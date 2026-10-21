package hub

import (
	"context"
	"sort"

	"github.com/rupaai/homelab/internal/core"
)

// Kinds of disk.
const (
	DiskData    = "data"    // where Homelab keeps its own data
	DiskStorage = "storage" // where Homelab keeps files (config.yaml storage:)
	DiskFolder  = "folder"  // a shared folder apps use (config.yaml folders:)
)

// DiskView is one disk and how full it is.
type DiskView struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Used    uint64 `json:"used"`
	Total   uint64 `json:"total"`
	Problem string `json:"problem,omitempty"`
	// Shared means it's on the same disk as one listed before it, so it isn't counted twice.
	Shared bool `json:"shared,omitempty"`
}

// Disks lists Homelab's data disk, its storage and the shared folders, with free space.
func (h *Hub) Disks(ctx context.Context) []DiskView {
	list := []DiskView{{Name: "homelab", Path: h.cfg.DataDir, Kind: DiskData}}
	for _, d := range h.cfg.Storage {
		list = append(list, DiskView{Name: d.Name, Path: d.Path, Kind: DiskStorage})
	}
	names := make([]string, 0, len(h.cfg.Folders))
	for n := range h.cfg.Folders {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		list = append(list, DiskView{Name: n, Path: h.cfg.Folders[n], Kind: DiskFolder})
	}
	if h.ad.Resources == nil {
		return list
	}
	seen := map[core.DiskSpace]bool{}
	for i := range list {
		d := &list[i]
		sp, err := h.ad.Resources.Disk(ctx, d.Path)
		if err != nil {
			d.Problem = err.Error()
			continue
		}
		d.Used, d.Total = sp.Used, sp.Total
		// Two places on one disk report the same numbers.
		d.Shared = seen[sp]
		seen[sp] = true
	}
	return list
}
