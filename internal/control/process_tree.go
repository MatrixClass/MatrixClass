package control

// commonProtectedProcesses are never terminated by policy enforcement on any platform.
var commonProtectedProcesses = []string{
	"nexlab",
	"nexlab.exe",
	"nexlab-student",
	"nexlab-student.exe",
	"nexlab-teacher",
	"nexlab-teacher.exe",
}

// maxProcessTreeDepth guards parent-chain walks against cycles or corrupt data.
const maxProcessTreeDepth = 1024

// processLineage computes the set of PIDs that must never be terminated because
// they are selfPID itself, one of its ancestors (the parent chain up to init),
// or one of its descendants. parents maps pid -> parent pid; unknown entries are
// treated as unrelated.
func processLineage(selfPID uint32, parents map[uint32]uint32) map[uint32]bool {
	related := make(map[uint32]bool)
	if selfPID == 0 {
		return related
	}
	related[selfPID] = true
	// Parent chain.
	cur := selfPID
	for i := 0; i < maxProcessTreeDepth; i++ {
		parent, ok := parents[cur]
		if !ok || parent == 0 || parent == cur || related[parent] {
			break
		}
		related[parent] = true
		cur = parent
	}
	// Descendants: a pid is a descendant when walking its parent chain reaches selfPID.
	isDescendant := make(map[uint32]bool)
	for pid := range parents {
		if related[pid] {
			continue
		}
		var chain []uint32
		found := false
		cur := pid
		for i := 0; i < maxProcessTreeDepth; i++ {
			if known, ok := isDescendant[cur]; ok {
				found = known
				break
			}
			chain = append(chain, cur)
			parent, ok := parents[cur]
			if !ok || parent == 0 || parent == cur {
				break
			}
			if parent == selfPID {
				found = true
				break
			}
			cur = parent
		}
		for _, p := range chain {
			isDescendant[p] = found
		}
	}
	for pid, desc := range isDescendant {
		if desc {
			related[pid] = true
		}
	}
	return related
}

// isAncestorOrDescendant reports whether pid is selfPID, in selfPID's parent
// chain, or has selfPID in its own parent chain, using parentOf to look up the
// parent of a single process (ok=false when unknown).
func isAncestorOrDescendant(selfPID, pid uint32, parentOf func(uint32) (uint32, bool)) bool {
	if selfPID == 0 || pid == 0 {
		return false
	}
	if pid == selfPID {
		return true
	}
	cur := selfPID
	for i := 0; i < maxProcessTreeDepth; i++ {
		parent, ok := parentOf(cur)
		if !ok || parent == 0 || parent == cur {
			break
		}
		if parent == pid {
			return true
		}
		cur = parent
	}
	cur = pid
	for i := 0; i < maxProcessTreeDepth; i++ {
		parent, ok := parentOf(cur)
		if !ok || parent == 0 || parent == cur {
			break
		}
		if parent == selfPID {
			return true
		}
		cur = parent
	}
	return false
}
