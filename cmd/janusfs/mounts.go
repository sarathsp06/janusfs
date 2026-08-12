package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	var pick bool
	cmd := &cobra.Command{
		Use:   "mounts",
		Short: "List active and recorded JanusFS mounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if pick {
				return runMountsPick()
			}
			return runMounts(jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	cmd.Flags().BoolVarP(&pick, "interactive", "i", false, "fuzzy-pick a live mount and open its dashboard")
	return cmd
}

// runMountsPick fuzzy-picks among live mounts and opens the chosen dashboard.
func runMountsPick() error {
	if !interactive {
		return errors.New("mounts -i needs a terminal")
	}
	var live []mountListing
	for _, m := range collectMountListings() {
		if m.Status == "mounted" && m.Dashboard != "" {
			live = append(live, m)
		}
	}
	if len(live) == 0 {
		fmt.Println("No live mounts to open.")
		return nil
	}
	items := make([]string, len(live))
	for i, m := range live {
		items[i] = fmt.Sprintf("%s  %s", m.Mountpoint, cDim(m.Src))
	}
	idx, err := pickOne("open dashboard", items)
	if err != nil || idx < 0 {
		return err
	}
	url := live[idx].Dashboard
	name, args, ok := browserOpenCommand(url)
	if !ok {
		fmt.Printf("%s Dashboard: %s\n", symGood(), url)
		return nil
	}
	fmt.Printf("%s Opening %s\n", symGood(), url)
	return exec.Command(name, args...).Start()
}

// collectMountListings gathers the current mount picture (live daemon mounts
// merged with recorded mounts), shared by `mounts` and the interactive pickers.
func collectMountListings() []mountListing {
	records, _ := config.LoadMounts()
	var live []mountStatus
	if resp, err := callDaemon("mounts", daemonRequest{Cmd: "list"}); err == nil && resp.OK {
		live = resp.Mounts
	}
	return classifyMountRecords(live, records, defaultStatDir, mountpointMounted)
}

func runMounts(jsonOut bool) error {
	listings := collectMountListings()
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
