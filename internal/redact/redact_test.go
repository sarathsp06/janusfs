package redact

import (
	"bytes"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/sarathsp06/janusfs/internal/patterns"
)

func mustPatterns(t *testing.T, refs ...string) []*patterns.Pattern {
	t.Helper()
	var out []*patterns.Pattern
	for _, ref := range refs {
		ps, err := patterns.ParsePatternRef(ref)
		if err != nil {
			t.Fatalf("ParsePatternRef(%q): %v", ref, err)
		}
		out = append(out, ps...)
	}
	return out
}

func TestRedactWholeFileSentinel(t *testing.T) {
	pats := mustPatterns(t, patterns.WholeFileName)
	in := []byte("hello world, this is secret content")
	out := Redact(in, pats)
	if len(out) != len(in) {
		t.Fatalf("len(out)=%d, want %d", len(out), len(in))
	}
	for _, b := range out {
		if b != '*' {
			t.Fatalf("expected every byte masked, got %q", out)
		}
	}
}

func TestRedactEnvValue(t *testing.T) {
	pats := mustPatterns(t, "env-value")
	in := []byte("API_KEY=supersecret123\nDEBUG=true\n")
	out := Redact(in, pats)
	if len(out) != len(in) {
		t.Fatalf("len(out)=%d, want %d", len(out), len(in))
	}
	got := string(out)
	if !strings.Contains(got, "API_KEY=") || strings.Contains(got, "supersecret123") {
		t.Fatalf("expected value redacted, key preserved, got %q", got)
	}
	if !strings.Contains(got, "DEBUG=") || strings.Contains(got, "true") {
		t.Fatalf("expected second value redacted too, got %q", got)
	}
}

func TestRedactGroupIndexZeroWholeMatchMasked(t *testing.T) {
	pats := mustPatterns(t, "private-key")
	in := []byte("prefix\n-----BEGIN RSA PRIVATE KEY-----\nABCD1234\n-----END RSA PRIVATE KEY-----\nsuffix\n")
	out := Redact(in, pats)
	if len(out) != len(in) {
		t.Fatalf("len(out)=%d, want %d", len(out), len(in))
	}
	got := string(out)
	if !strings.HasPrefix(got, "prefix\n") || !strings.HasSuffix(got, "suffix\n") {
		t.Fatalf("expected prefix/suffix preserved, got %q", got)
	}
	if strings.Contains(got, "ABCD1234") || strings.Contains(got, "BEGIN RSA") {
		t.Fatalf("expected PEM block fully masked, got %q", got)
	}
}

func TestFindSpansUnionAcrossPatterns(t *testing.T) {
	pats := mustPatterns(t, "aws-key", "github-token")
	in := []byte("AKIAABCDEFGHIJKLMNOP and ghp_" + strings.Repeat("a", 36))
	spans := FindSpans(in, 0, pats)
	if len(spans) != 2 {
		t.Fatalf("expected 2 disjoint spans, got %v", spans)
	}
}

func TestMergeSpansCoalescesOverlap(t *testing.T) {
	got := mergeSpans([]Span{{Off: 0, Len: 5}, {Off: 3, Len: 5}, {Off: 20, Len: 2}})
	want := []Span{{Off: 0, Len: 8}, {Off: 20, Len: 2}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("mergeSpans = %v, want %v", got, want)
	}
}

// TestRedactLenPreservedProperty asserts the core output invariant:
// len(out) == len(in), fuzzed across the builtin pattern library and
// random inputs (including ones with no matches at all).
func TestRedactLenPreservedProperty(t *testing.T) {
	allBuiltins := []string{"env-value", "aws-key", "private-key", "jwt", "db-uri", "github-token", "generic-secret"}
	pats := mustPatterns(t, allBuiltins...)

	rng := rand.New(rand.NewSource(1))
	alphabet := []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789=:/-_.\n \t")
	for i := 0; i < 200; i++ {
		n := rng.Intn(500)
		buf := make([]byte, n)
		for j := range buf {
			buf[j] = alphabet[rng.Intn(len(alphabet))]
		}
		out := Redact(buf, pats)
		if len(out) != len(buf) {
			t.Fatalf("iteration %d: len(out)=%d, want %d (input %q)", i, len(out), len(buf), buf)
		}
	}
}

// TestRedactIdempotent: redacting already-redacted output changes nothing
// further, because none of the builtin patterns match a run of '*' characters.
func TestRedactIdempotent(t *testing.T) {
	allBuiltins := []string{"env-value", "aws-key", "private-key", "jwt", "db-uri", "github-token", "generic-secret"}
	pats := mustPatterns(t, allBuiltins...)

	in := []byte("API_KEY=AKIAABCDEFGHIJKLMNOP\npassword: hunter2hunter2\n")
	once := Redact(in, pats)
	twice := Redact(once, pats)
	if !bytes.Equal(once, twice) {
		t.Fatalf("redact is not idempotent:\n once=%q\ntwice=%q", once, twice)
	}
}

func TestStreamChunkedMatchesWholeBufferRedact(t *testing.T) {
	pats := mustPatterns(t, "aws-key", "github-token")

	rng := rand.New(rand.NewSource(2))
	var buf bytes.Buffer
	buf.WriteString(strings.Repeat("filler filler filler ", 5000))
	buf.WriteString("AKIAABCDEFGHIJKLMNOP")
	buf.WriteString(strings.Repeat("more filler text here ", 5000))
	buf.WriteString("ghp_" + strings.Repeat("z", 40))
	buf.WriteString(strings.Repeat("trailing filler ", 100))
	in := buf.Bytes()
	_ = rng

	want := Redact(in, pats)

	var out bytes.Buffer
	if err := Stream(&out, bytes.NewReader(in), pats, 10<<20); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("streamed output diverges from whole-buffer Redact:\nstream len=%d want len=%d", out.Len(), len(want))
	}
}

// TestStreamChunkBoundaryTorture plants a secret exactly straddling the
// 256 KiB chunk boundary (and near-boundary offsets on both sides,
// including inside a multibyte UTF-8 rune) to verify the carry-over
// mechanism catches it rather than silently missing a match split across
// two Read calls.
func TestStreamChunkBoundaryTorture(t *testing.T) {
	pats := mustPatterns(t, "aws-key")
	secret := "AKIAABCDEFGHIJKLMNOP" // 20 bytes, aws-key's fixed shape

	offsets := []int{
		chunkSize - 15, // straddles: starts before boundary, ends after
		chunkSize - 1,
		chunkSize,
		chunkSize + 1,
	}

	for _, off := range offsets {
		t.Run("offset_"+strconv.Itoa(off), func(t *testing.T) {
			filler := strings.Repeat("x", off)
			// Splice a multibyte rune right at the filler/secret join so the
			// boundary region also exercises non-ASCII bytes, so a
			// multibyte UTF-8 sequence straddling the cut is covered.
			// A space after the secret gives aws-key's trailing \b a
			// word/non-word transition to fire on — "P" followed directly
			// by "y" (both word chars) would never match the boundary
			// regardless of chunking, which isn't the straddle behavior
			// this test is after.
			in := []byte(filler + "€" + secret + " " + strings.Repeat("y", 1000))

			var out bytes.Buffer
			if err := Stream(&out, bytes.NewReader(in), pats, 10<<20); err != nil {
				t.Fatalf("Stream: %v", err)
			}
			if out.Len() != len(in) {
				t.Fatalf("len(out)=%d, want %d (offset=%d)", out.Len(), len(in), off)
			}
			if strings.Contains(out.String(), secret) {
				t.Fatalf("secret survived at offset %d: %.40s...", off, out.String()[off:])
			}
			want := Redact(in, pats)
			if !bytes.Equal(out.Bytes(), want) {
				t.Fatalf("offset %d: streamed output diverges from whole-buffer Redact", off)
			}
		})
	}
}

func TestStreamWholeFileModeForPrivateKeyAndCustomUnanchored(t *testing.T) {
	pats := mustPatterns(t, "private-key")
	m, _ := classify(pats)
	if m != modeWholeFile {
		t.Fatalf("private-key should classify as modeWholeFile, got %v", m)
	}

	custom := mustPatterns(t, "/secret=(.+)/")
	m2, _ := classify(custom)
	if m2 != modeWholeFile {
		t.Fatalf("non-line-anchored custom regex should classify as modeWholeFile, got %v", m2)
	}

	lineAnchored := mustPatterns(t, `/(?m)^secret=(.+)$/`)
	m3, _ := classify(lineAnchored)
	if m3 != modeLine {
		t.Fatalf("line-anchored custom regex should classify as modeLine, got %v", m3)
	}
}

func TestStreamBufferExceededFailsClosed(t *testing.T) {
	pats := mustPatterns(t, "private-key") // forces modeWholeFile
	in := bytes.Repeat([]byte("a"), 1000)
	var out bytes.Buffer
	err := Stream(&out, bytes.NewReader(in), pats, 100)
	if err != ErrBufferExceeded {
		t.Fatalf("expected ErrBufferExceeded, got %v", err)
	}
}

func TestStreamLineModeRedactsAcrossChunks(t *testing.T) {
	pats := mustPatterns(t, `/(?m)^secret=(.+)$/`)
	var buf bytes.Buffer
	buf.WriteString(strings.Repeat("noise\n", 100000)) // push well past one 256 KiB chunk
	buf.WriteString("secret=topsecretvalue\n")
	buf.WriteString(strings.Repeat("more noise\n", 10))
	in := buf.Bytes()

	var out bytes.Buffer
	if err := Stream(&out, bytes.NewReader(in), pats, 10<<20); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if out.Len() != len(in) {
		t.Fatalf("len(out)=%d, want %d", out.Len(), len(in))
	}
	if strings.Contains(out.String(), "topsecretvalue") {
		t.Fatal("secret value survived line-mode streaming")
	}
	if !strings.Contains(out.String(), "secret=") {
		t.Fatal("expected key preserved (group-1-only masking)")
	}
}

func BenchmarkRedactDotenvLike(b *testing.B) {
	// Replicate 1 MB synthetic dotenv-like corpus: 20,000 lines
	var sb strings.Builder
	for i := 0; i < 20000; i++ {
		sb.WriteString("API_KEY=supersecret123\n")
	}
	corpus := []byte(sb.String())

	pats, err := patterns.ParsePatternRef("env-value")
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Redact(corpus, pats)
	}
}

func BenchmarkRedactNoMatch(b *testing.B) {
	// 20,000 lines of plain text without any matching env-value pattern
	var sb strings.Builder
	for i := 0; i < 20000; i++ {
		sb.WriteString("This is a clean line of text with some plain content that does not have any env assignments.\n")
	}
	corpus := []byte(sb.String())

	pats, err := patterns.ParsePatternRef("env-value")
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Redact(corpus, pats)
	}
}
