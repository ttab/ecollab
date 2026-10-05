package ecollab

// Test-only access to the delete set reader, which is unexported
// because nothing outside the client needs it.

// TestDeleteSet is a delete set as the tests compare it: per client,
// [clock, length] ranges, sorted and merged.
type TestDeleteSet map[uint64][][2]uint64

func toTestDeleteSet(ds deleteSet) TestDeleteSet {
	out := make(TestDeleteSet, len(ds))

	for client, rs := range ds {
		for _, r := range rs {
			out[client] = append(out[client], [2]uint64{r.clock, r.length})
		}
	}

	return out
}

func fromTestDeleteSet(ds TestDeleteSet) deleteSet {
	out := make(deleteSet, len(ds))

	for client, rs := range ds {
		for _, r := range rs {
			out.add(client, r[0], r[1])
		}
	}

	return out
}

// UpdateV1DeleteSet exposes updateV1DeleteSet.
func UpdateV1DeleteSet(update []byte) (bool, TestDeleteSet, error) {
	hasStructs, ds, err := updateV1DeleteSet(update)
	if err != nil {
		return false, nil, err
	}

	return hasStructs, toTestDeleteSet(ds), nil
}

// NormalizeDeleteSet merges a test delete set's ranges.
func NormalizeDeleteSet(ds TestDeleteSet) TestDeleteSet {
	return toTestDeleteSet(fromTestDeleteSet(ds))
}

// SubtractDeleteSets exposes deleteSet.subtract.
func SubtractDeleteSets(a, b TestDeleteSet) TestDeleteSet {
	return toTestDeleteSet(fromTestDeleteSet(a).subtract(fromTestDeleteSet(b)))
}
