package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/config"
)

var errMountListingMissing = errors.New("missing source")

type mountListing struct {
	Status     string `json:"status"`
	Src        string `json:"src"`
	Mountpoint string `json:"mountpoint"`
	Label      string `json:"label,omitempty"`
	Dashboard  string `json:"dashboard,omitempty"`
	Error      string `json:"error,omitempty"`
}

type mountListingsResponse struct {
	Mounts []mountListing `json:"mounts"`
}

type statDirFunc func(string) error
type isMountedFunc func(string) bool

func newMountsCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "mounts",
		Short: "List active and recorded JanusFS mounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMounts(jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

func runMounts(jsonOut bool) error {
	records, _ := config.LoadMounts()
	var live []mountStatus
	if resp, err := callDaemon("mounts", daemonRequest{Cmd: "list"}); err == nil && resp.OK {
		live = resp.Mounts
	}
	listings := classifyMountRecords(live, records, defaultStatDir, mountpointMounted)
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(mountListingsResponse{Mounts: listings})
	}
	printMountListings(os.Stdout, listings)
	return nil
}

func defaultStatDir(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return errMountListingMissing
	}
	if !st.IsDir() {
		return errMountListingMissing
	}
	return nil
}

func classifyMountRecords(live []mountStatus, records []config.MountRecord, statDir statDirFunc, isMounted isMountedFunc) []mountListing {
	byMountpoint := map[string]mountListing{}
	for _, m := range live {
		byMountpoint[m.Mountpoint] = mountListing{Status: "mounted", Src: m.Src, Mountpoint: m.Mountpoint, Label: m.Label, Dashboard: m.Dashboard}
	}
	for _, rec := range records {
		if _, ok := byMountpoint[rec.Mountpoint]; ok {
			continue
		}
		listing := mountListing{Src: rec.Src, Mountpoint: rec.Mountpoint, Label: rec.Label}
		if err := statDir(rec.Src); err != nil {
			listing.Status = "missing-src"
			listing.Error = err.Error()
		} else if isMounted(rec.Mountpoint) {
			listing.Status = "stale"
		} else {
			listing.Status = "recorded"
		}
		byMountpoint[rec.Mountpoint] = listing
	}
	out := make([]mountListing, 0, len(byMountpoint))
	for _, listing := range byMountpoint {
		out = append(out, listing)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return out[i].Status < out[j].Status
		}
		return out[i].Mountpoint < out[j].Mountpoint
	})
	return out
}

func printMountListings(w io.Writer, listings []mountListing) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tSOURCE\tMOUNTPOINT\tDASHBOARD")
	for _, m := range listings {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", m.Status, m.Src, m.Mountpoint, m.Dashboard)
	}
	_ = tw.Flush()
}
