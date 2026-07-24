// Package redact implements SPEC.md §8's masking pipeline: finding the
// byte spans a pattern set matches in a buffer (§8.1) and replacing them
// with '*' (0x2A) while preserving length exactly (SPEC §2's
// "byte-length-preserving replacement" definition of Redaction).
//
// Two entry points cover SPEC §8.2's two cases: Redact operates on a whole
// in-memory buffer (used directly for anything within --cache-max-file,
// and as the primitive the streaming path below runs per-chunk). Stream
// handles files that don't fit the cache (NFR-4) or are populating the
// cache for the first time, bounding peak memory via 256 KiB chunking with
// a carry-over tail sized from the pattern set's matchable span — falling
// back to whole-file buffering (capped by maxBufferBytes) for pattern sets
// that include an unbounded match (the FR-16 whole-file/private-key
// sentinels, or a custom regex with no computable bound), per §8.2.
package redact

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/sarathsp06/janusfs/internal/patterns"
)

// chunkSize is the read granularity for Stream (SPEC §8.2: "Read the
// source in 256 KiB chunks").
const chunkSize = 256 * 1024

// Span is one absolute byte range to mask, per SPEC §8.1's
// CompiledPattern.FindSpans contract: Off is an absolute offset into the
// logical file (base + in-buffer offset), Len is the span's byte length.
type Span struct {
	Off int64
	Len int64
}

// FindSpans returns the merged (sorted, coalesced) union of every pattern
// in pats matching buf, as absolute offsets (base + in-buffer offset) —
// FR-14: "Overlapping matches from multiple patterns are unioned." A
// WholeFile pattern short-circuits to a single span covering all of buf,
// since it needs no matching (SPEC §8.1).
func FindSpans(buf []byte, base int64, pats []*patterns.Pattern) []Span {
	for _, p := range pats {
		if p.WholeFile {
			return []Span{{Off: base, Len: int64(len(buf))}}
		}
	}

	var spans []Span
	for _, p := range pats {
		if p.PreFilter != nil && !p.PreFilter(buf) {
			continue
		}
		for _, m := range p.Regex.FindAllSubmatchIndex(buf, -1) {
			start, end := m[0], m[1]
			if p.GroupIndex > 0 {
				gi := 2 * p.GroupIndex
				if gi+1 < len(m) && m[gi] >= 0 {
					start, end = m[gi], m[gi+1]
				}
			}
			if end > start {
				spans = append(spans, Span{Off: base + int64(start), Len: int64(end - start)})
			}
		}
	}
	return mergeSpans(spans)
}

// mergeSpans sorts spans by offset and coalesces overlapping/adjacent
// ranges into their union (FR-14).
func mergeSpans(spans []Span) []Span {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Off < spans[j].Off })

	out := make([]Span, 0, len(spans))
	cur := spans[0]
	for _, s := range spans[1:] {
		curEnd := cur.Off + cur.Len
		if s.Off <= curEnd {
			if end := s.Off + s.Len; end > curEnd {
				cur.Len = end - cur.Off
			}
			continue
		}
		out = append(out, cur)
		cur = s
	}
	return append(out, cur)
}

// Redact returns a copy of buf with every span pats matches replaced by
// '*' (0x2A), byte-length preserved exactly. Safe to call with an empty or
// nil pats (returns an unmodified copy).
func Redact(buf []byte, pats []*patterns.Pattern) []byte {
	spans := FindSpans(buf, 0, pats)
	if len(spans) == 0 {
		return buf
	}
	out := bytes.Clone(buf)
	for _, s := range spans {
		for i := s.Off; i < s.Off+s.Len; i++ {
			out[i] = '*'
		}
	}
	return out
}

// mode classifies how a pattern set must be processed for streaming
// (SPEC §8.2).
type mode int

const (
	// modeChunked carries over a bounded tail between 256 KiB chunks —
	// safe because every pattern in the set has a known-bounded match
	// length, so a match cannot straddle more than carryLen bytes across a
	// chunk boundary undetected.
	modeChunked mode = iota
	// modeLine buffers up to the next newline before processing — used
	// for a custom line-anchored ("(?m)") regex with no computable bound
	// (SPEC §8.2).
	modeLine
	// modeWholeFile buffers the entire input (capped by maxBufferBytes)
	// before processing — used for the whole-file/private-key sentinels
	// (SPEC §8.1: "those patterns force whole-file buffering mode") and
	// any other unbounded, non-line-anchored custom regex.
	modeWholeFile
)

// builtinCarryLen gives each bounded builtin (SPEC FR-16) a generous
// carry-over length: large enough to catch a realistic match of that
// shape even if it lands right at a chunk boundary. private-key is
// unbounded (a PEM block can be arbitrarily long) and is handled by
// modeWholeFile instead, alongside the whole-file sentinel itself.
var builtinCarryLen = map[string]int{
	"env-value":      4096,
	"aws-key":        128,
	"jwt":            8192,
	"db-uri":         4096,
	"github-token":   256,
	"generic-secret": 4096,
}

// classify picks the streaming mode + carry-over length for a pattern set
// (SPEC §8.2). The whole set is classified once, using the most
// conservative mode any single pattern requires: a single unbounded,
// non-line-anchored pattern forces modeWholeFile for the entire set, since
// chunked/line processing cannot safely bound that pattern's match length.
func classify(pats []*patterns.Pattern) (m mode, carryLen int) {
	m = modeChunked
	for _, p := range pats {
		if p.WholeFile {
			return modeWholeFile, 0
		}
		if !p.Builtin {
			if strings.HasPrefix(p.Regex.String(), "(?m)") {
				if m == modeChunked {
					m = modeLine
				}
				continue
			}
			return modeWholeFile, 0
		}
		if p.Name == "private-key" {
			return modeWholeFile, 0
		}
		if l, ok := builtinCarryLen[p.Name]; ok && l > carryLen {
			carryLen = l
		}
	}
	if carryLen == 0 {
		carryLen = 4096
	}
	return m, carryLen
}

// Stream reads all of r, redacts it against pats, and writes size-preserving
// output to w, per SPEC §8.2. maxBufferBytes bounds whole-file buffering
// (modeWholeFile / a stalled modeLine scan with no newline in sight):
// exceeding it returns ErrBufferExceeded rather than buffering unbounded
// memory (SPEC §8.2: "--redact-buffer-max ... beyond that the file fails
// closed to HIDDEN + warning" — the caller is responsible for that
// fail-closed mapping, this function only enforces the cap).
func Stream(w io.Writer, r io.Reader, pats []*patterns.Pattern, maxBufferBytes int64) error {
	m, carryLen := classify(pats)
	switch m {
	case modeWholeFile:
		return streamWholeFile(w, r, pats, maxBufferBytes)
	case modeLine:
		return streamLines(w, r, pats, maxBufferBytes)
	default:
		return streamChunked(w, r, pats, carryLen)
	}
}

// ErrBufferExceeded is returned by Stream when buffering (modeWholeFile,
// or an over-long line in modeLine) would exceed maxBufferBytes.
var ErrBufferExceeded = fmt.Errorf("redact: buffer exceeded --redact-buffer-max")

func streamWholeFile(w io.Writer, r io.Reader, pats []*patterns.Pattern, maxBufferBytes int64) error {
	buf, err := io.ReadAll(io.LimitReader(r, maxBufferBytes+1))
	if err != nil {
		return fmt.Errorf("redact: reading input: %w", err)
	}
	if int64(len(buf)) > maxBufferBytes {
		return ErrBufferExceeded
	}
	_, err = w.Write(Redact(buf, pats))
	return err
}

func streamLines(w io.Writer, r io.Reader, pats []*patterns.Pattern, maxBufferBytes int64) error {
	var buf bytes.Buffer
	chunk := make([]byte, chunkSize)
	for {
		n, rerr := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			if int64(buf.Len()) > maxBufferBytes {
				return ErrBufferExceeded
			}
			if err := flushCompleteLines(w, &buf, pats); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			_, err := w.Write(Redact(buf.Bytes(), pats))
			return err
		}
		if rerr != nil {
			return fmt.Errorf("redact: reading input: %w", rerr)
		}
	}
}

// flushCompleteLines redacts and writes every complete line currently in
// buf (everything up to and including the last '\n'), leaving any trailing
// partial line in buf for the next round.
func flushCompleteLines(w io.Writer, buf *bytes.Buffer, pats []*patterns.Pattern) error {
	data := buf.Bytes()
	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		return nil
	}
	if _, err := w.Write(Redact(data[:lastNL+1], pats)); err != nil {
		return err
	}
	rest := bytes.Clone(data[lastNL+1:])
	buf.Reset()
	buf.Write(rest)
	return nil
}

// streamChunked implements SPEC §8.2's core algorithm: read 256 KiB
// chunks, keep a carryLen-byte tail from the end of each processed region
// unprocessed so a match cannot straddle a chunk boundary undetected, and
// redact+flush everything before that tail.
func streamChunked(w io.Writer, r io.Reader, pats []*patterns.Pattern, carryLen int) error {
	var buf bytes.Buffer
	chunk := make([]byte, chunkSize)
	for {
		n, rerr := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			if err := flushExceptTail(w, &buf, pats, carryLen); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			_, err := w.Write(Redact(buf.Bytes(), pats))
			return err
		}
		if rerr != nil {
			return fmt.Errorf("redact: reading input: %w", rerr)
		}
	}
}

// flushExceptTail writes everything in buf except the last carryLen bytes,
// keeping that tail pending for the next round. It redacts against the
// *entire* buffered backlog, not just the committed prefix — a match
// straddling the cut point would otherwise be missed if we only showed the
// regex the prefix in isolation. Only the committed prefix is written; the
// carried tail keeps its original (unredacted) bytes, since it will be
// rescanned together with the next chunk's data.
func flushExceptTail(w io.Writer, buf *bytes.Buffer, pats []*patterns.Pattern, carryLen int) error {
	data := buf.Bytes()
	if len(data) <= carryLen {
		return nil
	}
	cut := len(data) - carryLen
	redacted := Redact(data, pats)
	if _, err := w.Write(redacted[:cut]); err != nil {
		return err
	}
	tail := bytes.Clone(data[cut:])
	buf.Reset()
	buf.Write(tail)
	return nil
}
