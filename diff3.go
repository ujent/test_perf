package git

import (
	"bufio"
	"fmt"
	"io"
	"math/rand"

	"gopkg.in/src-d/go-billy.v4"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
)

//Diff3 - interface provides functionality of three-way merging
type Diff3 interface {
	Merge(baseB, oursB, theirsB *object.Blob, fs billy.Filesystem) (*mergingFileInfo, error)
}

type fileLine struct {
	number int
	text   string
}

type diff3 struct {
	diffA []fileDiff
	diffB []fileDiff

	base []fileLine
	a    []fileLine
	b    []fileLine
}

type indexes struct {
	baseIndex int
	aIndex    int
	bIndex    int
}

type indexRange struct {
	from *indexes
	to   *indexes
}

type match struct {
	baseIndex int
	aIndex    int
	bIndex    int
}

//NewDiff3 - creates a new Diff3 without any setup
func NewDiff3() Diff3 {
	return &diff3{}
}

func (d *diff3) Merge(baseB, oursB, theirsB *object.Blob, fs billy.Filesystem) (*mergingFileInfo, error) {

	err := d.setup(baseB, oursB, theirsB)
	if err != nil {
		return nil, err
	}

	temp, err := fs.Create(fmt.Sprintf("temp_%d", rand.Int()))

	if err != nil {
		return nil, err
	}

	conflicts, err := d.writeChunks(&temp)

	if err != nil {
		temp.Close()
		fs.Remove(temp.Name())

		return nil, err
	}

	err = temp.Close()
	if err != nil {
		return nil, err
	}

	return &mergingFileInfo{path: temp.Name(), unResolvedConflicts: conflicts}, nil
}

func (d *diff3) setup(baseB, oursB, theirsB *object.Blob) error {
	mh := newMergeHelper()

	linesBase, err := mh.prepareBlob(baseB)
	if err != nil {
		return err
	}

	linesOurs, err := mh.prepareBlob(oursB)
	if err != nil {
		return err
	}

	linesTheirs, err := mh.prepareBlob(theirsB)
	if err != nil {
		return err
	}

	diffOurs, err := d.diffFiles(linesBase, linesOurs)
	if err != nil {
		return err
	}

	diffTheirs, err := d.diffFiles(linesBase, linesTheirs)
	if err != nil {
		return err
	}

	d.base = linesBase
	d.a = linesOurs
	d.b = linesTheirs
	d.diffA = diffOurs
	d.diffB = diffTheirs

	return nil
}

func (d *diff3) setupWithFiles(base, ours, theirs *billy.File) error {
	mh := newMergeHelper()

	baseLines, err := mh.prepareFile(base)
	if err != nil {
		return err
	}

	oursLines, err := mh.prepareFile(ours)
	if err != nil {
		return err
	}

	theirsLines, err := mh.prepareFile(theirs)
	if err != nil {
		return err
	}

	diffOurs, err := d.diffFiles(baseLines, oursLines)
	if err != nil {
		return err
	}

	diffTheirs, err := d.diffFiles(baseLines, theirsLines)
	if err != nil {
		return err
	}

	d.base = baseLines
	d.a = oursLines
	d.b = theirsLines
	d.diffA = diffOurs
	d.diffB = diffTheirs

	return nil
}

func (d *diff3) diffFiles(one, two []fileLine) ([]fileDiff, error) {

	md := NewMyersDifferer(one, two)
	diff := md.Diff()

	return diff, nil
}

func (d *diff3) getMatches(fd []fileDiff) map[int]int {

	matches := map[int]int{}

	for _, el := range fd {
		if el.diffType == fileDiffEql {
			matches[el.lineA.number] = el.lineB.number
		}
	}

	return matches
}

//writeChunks returns quantity of unresolved conflicts
func (d *diff3) writeChunks(f *billy.File) (int, error) {

	matchesA := d.getMatches(d.diffA)
	matchesB := d.getMatches(d.diffB)

	lineBase, lineA, lineB := 0, 0, 0
	conflicts := 0

	for {
		i := d.getNextMismatch(matchesA, matchesB, lineBase, lineA, lineB)

		if i == 0 {
			match := d.getNextMatch(matchesA, matchesB, lineBase)

			if match != nil {
				newIndexes, conf, err := d.emitChunk(&indexes{baseIndex: lineBase, aIndex: lineA, bIndex: lineB}, match, f)

				if err != nil {
					return 0, err
				}

				lineBase, lineA, lineB = newIndexes.baseIndex, newIndexes.aIndex, newIndexes.bIndex

				conflicts += conf

			} else {
				conf, err := d.emitFinalChunk(&indexes{aIndex: lineA, bIndex: lineB, baseIndex: lineBase}, f)

				if err != nil {
					return 0, err
				}

				conflicts += conf

				return conflicts, nil
			}
		} else if i != -1 {
			newIndexes, conf, err := d.emitChunk(&indexes{baseIndex: lineBase, aIndex: lineA, bIndex: lineB}, &match{baseIndex: lineBase + i, aIndex: lineA + i, bIndex: lineB + i}, f)

			if err != nil {
				return 0, err
			}

			lineBase, lineA, lineB = newIndexes.baseIndex, newIndexes.aIndex, newIndexes.bIndex
			conflicts += conf

		} else {
			conf, err := d.emitFinalChunk(&indexes{aIndex: lineA, bIndex: lineB, baseIndex: lineBase}, f)

			if err != nil {
				return 0, err
			}

			conflicts += conf

			return conflicts, nil
		}
	}

}

func (d *diff3) writeChunk(ir *indexRange, f *billy.File) (conflicts int, err error) {
	j := ir.from.aIndex
	k := ir.from.bIndex
	blockA := []string{}
	blockB := []string{}

	notEqlA := []fileLine{}
	notEqlB := []fileLine{}

	for i := ir.from.baseIndex; i < ir.to.baseIndex; i++ {
		baseLine := d.base[i]

		if j < ir.to.aIndex {
			aLine := d.a[j]

			if baseLine.text != aLine.text {

				notEqlA = append(notEqlA, aLine)
			}

			blockA = append(blockA, aLine.text)

			j++
		}

		if k < ir.to.bIndex {
			bLine := d.b[k]

			if baseLine.text != bLine.text {

				notEqlB = append(notEqlB, bLine)
			}

			blockB = append(blockB, bLine.text)

			k++
		}
	}

	for j < ir.to.aIndex {
		aLine := d.a[j]
		notEqlA = append(notEqlA, aLine)
		blockA = append(blockA, aLine.text)

		j++
	}

	for k < ir.to.bIndex {
		bLine := d.b[k]
		notEqlB = append(notEqlB, bLine)
		blockB = append(blockB, bLine.text)

		k++
	}

	mh := newMergeHelper()

	lenBase := ir.to.baseIndex - ir.from.baseIndex
	lenA := ir.to.aIndex - ir.from.aIndex
	lenB := ir.to.bIndex - ir.from.bIndex

	isEqlA := (lenBase < 1 && lenA < 1) || (lenBase == lenA && ir.from.aIndex != ir.to.aIndex && len(notEqlA) == 0)
	isEqlB := (lenBase < 1 && lenB < 1) || (lenBase == lenB && ir.from.bIndex != ir.to.bIndex && len(notEqlB) == 0)

	areBothEmpty := ir.from.aIndex >= ir.to.aIndex && ir.from.bIndex >= ir.to.bIndex

	if isEqlA && isEqlB {
		err := mh.writeBlockToFile(blockA, f)

		if err != nil {
			return 0, err
		}
	} else if isEqlA {
		err := mh.writeBlockToFile(blockB, f)

		if err != nil {
			return 0, err
		}
	} else if isEqlB {
		err := mh.writeBlockToFile(blockA, f)

		if err != nil {
			return 0, err
		}
	} else {
		if !areBothEmpty {
			if d.isBlockEqual(blockA, blockB) {
				err := mh.writeBlockToFile(blockA, f)

				if err != nil {
					return 0, err
				}
			} else {

				err = mh.writeConflictToFile(blockA, blockB, f)

				if err != nil {
					return 0, err
				}

				conflicts++
			}

		}

	}

	return conflicts, nil
}

func (d *diff3) isBlockEqual(blA, blB []string) bool {
	if len(blA) != len(blB) {
		return false
	}

	for i, a := range blA {
		if a != blB[i] {
			return false
		}
	}

	return true
}

func (d *diff3) emitChunk(currentPositions *indexes, match *match, f *billy.File) (newIndexes *indexes, conflicts int, err error) {

	baseTo := match.baseIndex
	aTo := match.aIndex
	bTo := match.bIndex

	ir := indexRange{
		from: currentPositions,
		to: &indexes{
			baseIndex: baseTo,
			aIndex:    aTo,
			bIndex:    bTo,
		},
	}

	conflicts, err = d.writeChunk(&ir, f)

	if err != nil {
		return nil, 0, err
	}

	return &indexes{baseIndex: match.baseIndex, aIndex: match.aIndex, bIndex: match.bIndex}, conflicts, nil
}

func (d *diff3) emitFinalChunk(from *indexes, f *billy.File) (conflicts int, err error) {
	baseTo := len(d.base)
	aTo := len(d.a)
	bTo := len(d.b)

	ir := indexRange{
		from: from,
		to: &indexes{
			baseIndex: baseTo,
			aIndex:    aTo,
			bIndex:    bTo,
		},
	}

	conflicts, err = d.writeChunk(&ir, f)

	if err != nil {
		return 0, err
	}

	return conflicts, nil
}

func (d *diff3) isInBounds(i, lineBase, lineA, lineB int) bool {
	return (lineBase+i) <= len(d.base) || (lineA+i) <= len(d.a) || (lineB+i) <= len(d.b)
}

func (d *diff3) getNextMismatch(matchesA, matchesB map[int]int, lineBase, lineA, lineB int) int {
	i := 0

	for d.isInBounds(i, lineBase, lineA, lineB) && d.isMatch(matchesA, lineBase, lineA, i) && d.isMatch(matchesB, lineBase, lineB, i) {
		i++
	}

	var res int
	if d.isInBounds(i, lineBase, lineA, lineB) {
		res = i
	} else {
		return -1
	}

	return res
}

func (d *diff3) getNextMatch(matchesA, matchesB map[int]int, lineBase int) *match {
	baseNum := lineBase

	for baseNum < len(d.base) {
		mA, okA := matchesA[baseNum]
		mB, okB := matchesB[baseNum]

		if okA && okB {
			return &match{baseIndex: baseNum, aIndex: mA, bIndex: mB}
		}

		baseNum++
	}

	return nil
}

func (d *diff3) isMatch(matches map[int]int, lineBase, offset, i int) bool {
	el, ok := matches[lineBase+i]

	if !ok {
		return false
	}

	return el == offset+i
}

type mergeHelper struct{}

func newMergeHelper() *mergeHelper {
	return &mergeHelper{}
}

func (mh *mergeHelper) writeBlockToFile(bl []string, f *billy.File) error {
	temp := *f

	for _, l := range bl {

		_, err := temp.Write(append([]byte(l), '\n'))

		if err != nil {
			return err
		}
	}

	return nil
}

func (mh *mergeHelper) writeConflictToFile(ours, theirs []string, f *billy.File) error {
	if len(ours) == 0 && len(theirs) == 0 {
		return nil
	}

	temp := *f
	_, err := temp.Write(delimStart)

	if err != nil {
		return err
	}

	for _, l := range ours {
		_, err = temp.Write(append([]byte(l), '\n'))

		if err != nil {
			return err
		}
	}

	_, err = temp.Write(delimMiddle)
	if err != nil {
		return err
	}

	for _, l := range theirs {
		_, err = temp.Write(append([]byte(l), '\n'))

		if err != nil {
			return err
		}
	}

	_, err = temp.Write(delimEnd)
	if err != nil {
		return err
	}

	return nil
}

func (mh *mergeHelper) prepareBlob(b *object.Blob) ([]fileLine, error) {
	readerB, err := b.Reader()
	if err != nil {
		return nil, err
	}

	res, err := mh.getFileLines(readerB)

	readerB.Close()

	if err != nil {
		return nil, err
	}

	return res, nil
}

func (mh *mergeHelper) prepareFile(f *billy.File) ([]fileLine, error) {
	res, err := mh.getFileLines(*f)

	if err != nil {
		return nil, err
	}

	return res, nil
}

func (mh *mergeHelper) getFileLines(r io.Reader) ([]fileLine, error) {
	sc := bufio.NewScanner(r)

	res := []fileLine{}
	i := 0

	for sc.Scan() {
		res = append(res, fileLine{number: i, text: sc.Text()})
		i++
	}

	err := sc.Err()
	if err != nil {
		return nil, err
	}

	return res, nil
}
