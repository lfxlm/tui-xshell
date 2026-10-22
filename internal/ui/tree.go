package ui

import (
	"sort"
	"strings"

	"tui-ssh-workbench/internal/model"
)

type groupEntry struct {
	id              string
	name            string
	hostCount       int
	lastSibling     bool
	ancestorHasNext []bool
	isAll           bool
	isFavoriteView  bool
	isRecentView    bool
	isShortcutBar   bool
}

func buildGroupEntries(groups []model.Group, hosts []model.Host, shortcutBarSets ...[]model.ShortcutBar) []groupEntry {
	children := make(map[string][]model.Group)
	groupIDs := make(map[string]bool, len(groups))
	directCounts := make(map[string]int, len(groups))
	for _, group := range groups {
		children[group.ParentId] = append(children[group.ParentId], group)
		groupIDs[group.Id] = true
	}
	for _, host := range hosts {
		if host.GroupId != "" {
			directCounts[host.GroupId]++
		}
	}
	for parentID := range children {
		sort.Slice(children[parentID], func(i, j int) bool {
			left := strings.ToLower(children[parentID][i].Name)
			right := strings.ToLower(children[parentID][j].Name)
			if left == right {
				return children[parentID][i].Id < children[parentID][j].Id
			}
			return left < right
		})
	}

	countCache := make(map[string]int, len(groups))
	var subtreeCount func(string, map[string]bool) int
	subtreeCount = func(id string, visiting map[string]bool) int {
		if count, ok := countCache[id]; ok {
			return count
		}
		if visiting[id] {
			return directCounts[id]
		}
		visiting[id] = true
		count := directCounts[id]
		for _, child := range children[id] {
			count += subtreeCount(child.Id, visiting)
		}
		delete(visiting, id)
		countCache[id] = count
		return count
	}

	favoriteCount, recentCount := 0, 0
	for _, host := range hosts {
		if host.Favorite {
			favoriteCount++
		}
		if !host.LastConnectedAt.IsZero() {
			recentCount++
		}
	}
	entries := []groupEntry{
		{name: "全部主机", hostCount: len(hosts), isAll: true},
		{name: "收藏", hostCount: favoriteCount, isFavoriteView: true},
		{name: "最近连接", hostCount: recentCount, isRecentView: true},
	}
	if len(shortcutBarSets) > 0 {
		hostIDs := make(map[string]bool, len(hosts))
		for _, host := range hosts {
			hostIDs[host.Id] = true
		}
		for _, bar := range shortcutBarSets[0] {
			count := 0
			for _, hostID := range bar.HostIDs {
				if hostIDs[hostID] {
					count++
				}
			}
			entries = append(entries, groupEntry{
				id: bar.Id, name: bar.Name, hostCount: count, isShortcutBar: true,
			})
		}
	}
	visited := make(map[string]bool, len(groups))
	var appendTree func(string, []bool)
	appendTree = func(parentID string, ancestors []bool) {
		childrenForParent := children[parentID]
		for index, group := range childrenForParent {
			if visited[group.Id] {
				continue
			}
			visited[group.Id] = true
			entries = append(entries, groupEntry{
				id:              group.Id,
				name:            group.Name,
				hostCount:       subtreeCount(group.Id, make(map[string]bool)),
				lastSibling:     index == len(childrenForParent)-1,
				ancestorHasNext: append([]bool(nil), ancestors...),
			})
			nextAncestors := append(append([]bool(nil), ancestors...), index < len(childrenForParent)-1)
			appendTree(group.Id, nextAncestors)
		}
	}
	appendTree("", nil)

	// Include any malformed/orphaned records so a damaged tree never hides a group.
	if len(visited) < len(groups) {
		orphans := make([]model.Group, 0, len(groups)-len(visited))
		for _, group := range groups {
			if !visited[group.Id] && !groupIDs[group.ParentId] {
				orphans = append(orphans, group)
			}
		}
		if len(orphans) == 0 {
			for _, group := range groups {
				if !visited[group.Id] {
					orphans = append(orphans, group)
				}
			}
		}
		sort.Slice(orphans, func(i, j int) bool {
			return strings.ToLower(orphans[i].Name) < strings.ToLower(orphans[j].Name)
		})
		for orphanIndex, group := range orphans {
			if visited[group.Id] {
				continue
			}
			visited[group.Id] = true
			entries = append(entries, groupEntry{
				id:              group.Id,
				name:            group.Name,
				hostCount:       subtreeCount(group.Id, make(map[string]bool)),
				lastSibling:     orphanIndex == len(orphans)-1,
				ancestorHasNext: []bool{},
			})
			appendTree(group.Id, []bool{orphanIndex < len(orphans)-1})
		}
	}
	return entries
}

func descendantGroupIDs(groups []model.Group, rootID string) map[string]bool {
	children := make(map[string][]string, len(groups))
	for _, group := range groups {
		children[group.ParentId] = append(children[group.ParentId], group.Id)
	}
	result := map[string]bool{rootID: true}
	var visit func(string)
	visit = func(id string) {
		for _, childID := range children[id] {
			if result[childID] {
				continue
			}
			result[childID] = true
			visit(childID)
		}
	}
	visit(rootID)
	return result
}
