package ecollab

import (
	"errors"
	"fmt"
	"slices"

	"github.com/ttab/ecollab/lib0"
)

// deleteSet is a Yjs delete set: per client, the clock ranges whose
// items are deleted. Kept sorted and merged, so two sets that delete
// the same items compare alike however they were encoded.
//
// The client needs one because a Yjs v1 diff carries the sender's
// whole delete set whatever state vector it was computed against: a
// state vector says which items a peer has, not which of them it has
// deleted. So a diff with no structs is not empty just because the
// peer has every item, and telling whether its deletions are news
// takes a record of which deletions the peer already holds.
type deleteSet map[uint64][]dsRange

type dsRange struct {
	clock  uint64
	length uint64
}

func (r dsRange) end() uint64 {
	return r.clock + r.length
}

// add records length items from clock as deleted.
func (ds deleteSet) add(client, clock, length uint64) {
	if length == 0 {
		return
	}

	ds[client] = mergeRanges(append(ds[client], dsRange{clock, length}))
}

// empty reports whether the set deletes nothing.
func (ds deleteSet) empty() bool {
	for _, rs := range ds {
		if len(rs) > 0 {
			return false
		}
	}

	return true
}

// subtract is ds without the deletions in other.
func (ds deleteSet) subtract(other deleteSet) deleteSet {
	out := make(deleteSet, len(ds))

	for client, rs := range ds {
		left := subtractRanges(rs, other[client])
		if len(left) > 0 {
			out[client] = left
		}
	}

	return out
}

func mergeRanges(rs []dsRange) []dsRange {
	slices.SortFunc(rs, func(a, b dsRange) int {
		switch {
		case a.clock < b.clock:
			return -1
		case a.clock > b.clock:
			return 1
		}

		return 0
	})

	out := rs[:0]

	for _, r := range rs {
		if n := len(out); n > 0 && r.clock <= out[n-1].end() {
			if r.end() > out[n-1].end() {
				out[n-1].length = r.end() - out[n-1].clock
			}

			continue
		}

		out = append(out, r)
	}

	return out
}

// subtractRanges is a without b; both sorted and merged.
func subtractRanges(a, b []dsRange) []dsRange {
	var out []dsRange

	j := 0

	for _, r := range a {
		start, end := r.clock, r.end()

		for j < len(b) && b[j].end() <= start {
			j++
		}

		for k := j; k < len(b) && b[k].clock < end; k++ {
			if b[k].clock > start {
				out = append(out, dsRange{start, b[k].clock - start})
			}

			start = max(start, b[k].end())
		}

		if start < end {
			out = append(out, dsRange{start, end - start})
		}
	}

	return out
}

// encodedDeleteSet decodes the delete set a structs-free v1 update
// carries: what goyjs.Doc.EncodeDiffV1 answers when it is handed the
// document's own state vector, which is a cheap way to read a
// document's whole delete set.
func encodedDeleteSet(update []byte) (deleteSet, error) {
	hasStructs, ds, err := updateV1DeleteSet(update)
	if err != nil {
		return nil, err
	}

	if hasStructs {
		return nil, errors.New("the update carries structs")
	}

	return ds, nil
}

// maxAnyDepth bounds the nesting of lib0 any values updateV1DeleteSet
// will walk, so a hostile update cannot exhaust the stack.
const maxAnyDepth = 256

// updateV1DeleteSet reads a Yjs v1 update far enough to say whether
// it carries any structs and what its delete set is. The structs are
// skipped rather than decoded: the delete set comes after them, and
// there is no way to reach it but to walk over them.
//
// The layout is Yjs's UpdateEncoderV1:
//
//	varuint groups
//	[ varuint structs, varuint client, varuint clock, struct * structs ] * groups
//	varuint clients
//	[ varuint client, varuint ranges, [ varuint clock, varuint len ] * ranges ] * clients
func updateV1DeleteSet(update []byte) (bool, deleteSet, error) {
	d := lib0.NewDecoder(update)

	groups, err := d.ReadVarUint()
	if err != nil {
		return false, nil, fmt.Errorf("struct group count: %w", err)
	}

	hasStructs := false

	for range groups {
		n, err := d.ReadVarUint()
		if err != nil {
			return false, nil, fmt.Errorf("struct count: %w", err)
		}

		// The client and its starting clock.
		if err := skipVarUints(d, 2); err != nil {
			return false, nil, fmt.Errorf("struct group header: %w", err)
		}

		for range n {
			hasStructs = true

			if err := skipStruct(d); err != nil {
				return false, nil, err
			}
		}
	}

	clients, err := d.ReadVarUint()
	if err != nil {
		return false, nil, fmt.Errorf("delete set client count: %w", err)
	}

	ds := make(deleteSet)

	for range clients {
		client, err := d.ReadVarUint()
		if err != nil {
			return false, nil, fmt.Errorf("delete set client: %w", err)
		}

		ranges, err := d.ReadVarUint()
		if err != nil {
			return false, nil, fmt.Errorf("delete set range count: %w", err)
		}

		for range ranges {
			clock, err := d.ReadVarUint()
			if err != nil {
				return false, nil, fmt.Errorf("delete set clock: %w", err)
			}

			length, err := d.ReadVarUint()
			if err != nil {
				return false, nil, fmt.Errorf("delete set length: %w", err)
			}

			ds.add(client, clock, length)
		}
	}

	if !d.EOF() {
		return false, nil, errors.New("trailing bytes after the delete set")
	}

	return hasStructs, ds, nil
}

const (
	infoOrigin      = 0x80
	infoRightOrigin = 0x40
	infoParentSub   = 0x20
	infoContentRef  = 0x1f

	refGC   = 0
	refSkip = 10

	typeRefXMLElement = 3
	typeRefXMLHook    = 5
)

func skipStruct(d *lib0.Decoder) error {
	b, err := d.ReadN(1)
	if err != nil {
		return fmt.Errorf("struct info: %w", err)
	}

	info := b[0]

	switch info & infoContentRef {
	case refGC, refSkip:
		return skipVarUints(d, 1)
	}

	if info&infoOrigin != 0 {
		if err := skipVarUints(d, 2); err != nil {
			return fmt.Errorf("item origin: %w", err)
		}
	}

	if info&infoRightOrigin != 0 {
		if err := skipVarUints(d, 2); err != nil {
			return fmt.Errorf("item right origin: %w", err)
		}
	}

	// An item with neither origin names its parent itself.
	if info&(infoOrigin|infoRightOrigin) == 0 {
		if err := skipParent(d, info&infoParentSub != 0); err != nil {
			return err
		}
	}

	return skipContent(d, info&infoContentRef)
}

// skipParent walks the parent an item names: a root type by name or
// a nested one by ID, and the map key under it when there is one.
func skipParent(d *lib0.Decoder, hasSub bool) error {
	named, err := d.ReadVarUint()
	if err != nil {
		return fmt.Errorf("item parent info: %w", err)
	}

	if named == 1 {
		_, err = d.ReadVarString()
	} else {
		err = skipVarUints(d, 2)
	}

	if err != nil {
		return fmt.Errorf("item parent: %w", err)
	}

	if hasSub {
		if _, err := d.ReadVarString(); err != nil {
			return fmt.Errorf("item parent sub: %w", err)
		}
	}

	return nil
}

func skipContent(d *lib0.Decoder, ref byte) error {
	var err error

	switch ref {
	case 1: // deleted
		err = skipVarUints(d, 1)
	case 2: // JSON
		err = skipCounted(d, func() error {
			return skipString(d)
		})
	case 3: // binary
		err = skipBuffer(d)
	case 4, 5: // string, embed
		_, err = d.ReadVarString()
	case 6: // format: key and JSON value
		if _, err = d.ReadVarString(); err == nil {
			_, err = d.ReadVarString()
		}
	case 7: // type
		var typeRef uint64

		typeRef, err = d.ReadVarUint()
		if err == nil && (typeRef == typeRefXMLElement || typeRef == typeRefXMLHook) {
			_, err = d.ReadVarString()
		}
	case 8: // any
		err = skipCounted(d, func() error {
			return skipAny(d, 0)
		})
	case 9: // subdocument: guid and options
		if _, err = d.ReadVarString(); err == nil {
			err = skipAny(d, 0)
		}
	default:
		return fmt.Errorf("unknown item content type %d", ref)
	}

	if err != nil {
		return fmt.Errorf("item content type %d: %w", ref, err)
	}

	return nil
}

// skipAny walks one lib0 any value.
func skipAny(d *lib0.Decoder, depth int) error {
	if depth > maxAnyDepth {
		return errors.New("any value nested too deeply")
	}

	b, err := d.ReadN(1)
	if err != nil {
		return fmt.Errorf("any tag: %w", err)
	}

	switch b[0] {
	case 127, 126, 121, 120: // undefined, null, false, true
		return nil
	case 125: // signed varint: the same continuation bit as a varuint
		return skipVarUints(d, 1)
	case 124: // float32
		_, err = d.ReadN(4)
	case 123, 122: // float64, bigint64
		_, err = d.ReadN(8)
	case 119: // string
		_, err = d.ReadVarString()
	case 118: // object
		err = skipCounted(d, func() error {
			if err := skipString(d); err != nil {
				return err
			}

			return skipAny(d, depth+1)
		})
	case 117: // array
		err = skipCounted(d, func() error {
			return skipAny(d, depth+1)
		})
	case 116: // bytes
		err = skipBuffer(d)
	default:
		return fmt.Errorf("unknown any tag %d", b[0])
	}

	if err != nil {
		return fmt.Errorf("any value: %w", err)
	}

	return nil
}

func skipString(d *lib0.Decoder) error {
	if _, err := d.ReadVarString(); err != nil {
		return fmt.Errorf("string: %w", err)
	}

	return nil
}

func skipVarUints(d *lib0.Decoder, n int) error {
	for range n {
		if _, err := d.ReadVarUint(); err != nil {
			return fmt.Errorf("varuint: %w", err)
		}
	}

	return nil
}

func skipCounted(d *lib0.Decoder, each func() error) error {
	n, err := d.ReadVarUint()
	if err != nil {
		return fmt.Errorf("count: %w", err)
	}

	for range n {
		if err := each(); err != nil {
			return err
		}
	}

	return nil
}

func skipBuffer(d *lib0.Decoder) error {
	n, err := d.ReadVarUint()
	if err != nil {
		return fmt.Errorf("buffer length: %w", err)
	}

	if n > uint64(len(d.Remaining())) {
		return fmt.Errorf("buffer: %w", lib0.ErrTruncated)
	}

	if _, err := d.ReadN(int(n)); err != nil { //nolint:gosec // bounded by the check above.
		return fmt.Errorf("buffer: %w", err)
	}

	return nil
}
