package git

import (
	"time"

	"gopkg.in/src-d/go-git.v4/plumbing/object"
)

const (
	markNone uint32 = 1 << iota
	markParent1
	markParent2
	markStale
	markResult
)

type prioritizedCommit struct {
	value *object.Commit
	flags uint32

	priority time.Time // The priority of the item in the queue.
	index    int       // The index of the item in the heap.
}

// A PriorityQueue implements heap.Interface and holds Items.
type PriorityQueue []*prioritizedCommit

func (pq PriorityQueue) Len() int { return len(pq) }

func (pq PriorityQueue) Less(i, j int) bool {
	// We want Pop to give us the highest, not lowest, priority so we use greater than here.
	return pq[i].priority.After(pq[j].priority)
}

func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *PriorityQueue) Push(x interface{}) {
	n := len(*pq)
	item := x.(*prioritizedCommit)
	var hasEl bool

	for _, el := range *pq {
		if el.value.Hash == item.value.Hash {
			el.flags |= item.flags
			hasEl = true

			break
		}
	}

	if !hasEl {
		item.index = n
		*pq = append(*pq, item)
	}
}

func (pq *PriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	item.index = -1 // for safety
	*pq = old[0 : n-1]

	return item
}

func (pq *PriorityQueue) interesting() bool {
	for _, el := range *pq {
		if (el.flags & markStale) == 0 {
			return true
		}
	}

	return false
}
