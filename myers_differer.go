package git

//Differer provides functionality of calculation differences between two files
type Differer interface {
	Diff() []fileDiff
	GetShortestPath() (trace [][]int)
	Backtrack(trace [][]int) <-chan backtrackEl
}

type myersDifferer struct {
	a []fileLine
	b []fileLine
}

type fileDiffType int

const (
	fileDiffIns fileDiffType = iota + 1
	fileDiffDel
	fileDiffEql
)

type fileDiff struct {
	lineA    *fileLine
	lineB    *fileLine
	diffType fileDiffType
}

//NewMyersDifferer - creates a new MyersDifferer and setup it
func NewMyersDifferer(a, b []fileLine) Differer {
	return &myersDifferer{a: a, b: b}
}

func (md *myersDifferer) GetShortestPath() (trace [][]int) {
	lenA, lenB := len(md.a), len(md.b)
	n := lenA
	m := lenB

	max := n + m
	vLen := 2*max + 1
	v := make([]int, vLen)

	for d := 0; d <= max; d++ {

		for k := -d; k <= d; k = k + 2 {
			var x int

			i1 := md.getIndexV(lenA, lenB, k-1)
			i2 := md.getIndexV(lenA, lenB, k+1)

			if k == -d || (k != d && v[i1] < v[i2]) {
				i := md.getIndexV(lenA, lenB, k+1)
				x = v[i]

			} else {
				i := md.getIndexV(lenA, lenB, k-1)
				x = v[i] + 1
			}

			y := x - k

			for x < n && y < m && md.a[x].text == md.b[y].text {
				x++
				y++
			}

			//transform negative k to positive indexes
			i := md.getIndexV(lenA, lenB, k)
			v[i] = x

			if x >= n && y >= m {
				temp := make([]int, vLen)
				copy(temp, v)
				trace = append(trace, temp)

				return trace
			}
		}

		temp := make([]int, vLen)
		copy(temp, v)
		trace = append(trace, temp)
	}

	return nil
}

func (md *myersDifferer) getIndexV(lenA, lenB, val int) int {
	max := lenA + lenB

	return val + max
}

type backtrackEl struct {
	prevX int
	prevY int
	x     int
	y     int
}

func (md *myersDifferer) Backtrack(trace [][]int) <-chan backtrackEl {

	ch := make(chan backtrackEl)

	go func() {
		lenA, lenB := len(md.a), len(md.b)
		x, y := lenA, lenB
		trLen := len(trace)

		for i := 0; i < trLen; i++ {
			d := trLen - i - 1 //reverse trace
			v := trace[d]

			k := x - y
			var prevK int

			j1 := md.getIndexV(lenA, lenB, k-1)
			j2 := md.getIndexV(lenA, lenB, k+1)

			if k == -d || (k != d && v[j1] < v[j2]) {
				prevK = k + 1
			} else {
				prevK = k - 1

			}

			j := md.getIndexV(lenA, lenB, prevK)
			prevX := v[j]
			prevY := prevX - prevK

			for x > prevX && y > prevY {
				ch <- backtrackEl{x: x, y: y, prevX: x - 1, prevY: y - 1}

				x, y = x-1, y-1
			}

			if d > 0 {
				ch <- backtrackEl{x: x, y: y, prevX: prevX, prevY: prevY}
			}

			x, y = prevX, prevY

		}
		close(ch)
	}()

	return ch
}

//Diff - compares two files and returnes information about differences between them
func (md *myersDifferer) Diff() []fileDiff {
	trace := md.GetShortestPath()
	ch := md.Backtrack(trace)
	lenA, lenB := len(md.a), len(md.b)
	var diff []fileDiff

	for res := range ch {

		var lineA, lineB fileLine
		if res.prevX < lenA {
			lineA = md.a[res.prevX]
		}

		if res.prevY < lenB {
			lineB = md.b[res.prevY]
		}

		if res.x == res.prevX {
			diff = append(diff, fileDiff{diffType: fileDiffIns, lineB: &lineB})
		} else if res.y == res.prevY {
			diff = append(diff, fileDiff{diffType: fileDiffDel, lineA: &lineA})
		} else {
			diff = append(diff, fileDiff{diffType: fileDiffEql, lineA: &lineA, lineB: &lineB})
		}
	}

	return diff
}
