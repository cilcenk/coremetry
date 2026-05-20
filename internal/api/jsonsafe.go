package api

import (
	"math"
	"reflect"
)

// sanitizeFloats walks v in-place and replaces every float64 +
// float32 NaN/+Inf/-Inf value with 0. Used immediately before
// json.Marshal so a single rogue float from a CH quantilesMerge
// edge case or a divide-by-zero in derived metrics doesn't 500
// the entire response (encoding/json hard-errors on NaN+Inf per
// RFC 8259).
//
// v0.5.303 — Operator-reported (test env): even after the
// per-call-site safeF guards added in v0.5.301, some path still
// leaked NaN into the bundle response. Rather than chase every
// individual Scan in the codebase forever, this is a defence-
// in-depth pass at the JSON boundary.
//
// Walks: ptr, interface, struct, slice, array. Map values are
// handled by reflecting on the map and replacing entries that
// contain non-finite floats. Channels / funcs / unsupported
// kinds are silently skipped — they won't marshal anyway.
//
// Cost: ~one reflect.Value walk per response. For a typical
// bundle (≤500 OperationSummary rows + ServiceSummary +
// Problems) this is sub-millisecond. The amortised cost is
// hidden behind the existing 60s serveCached TTL anyway.
func sanitizeFloats(v any) {
	if v == nil {
		return
	}
	rv := reflect.ValueOf(v)
	scrub(rv)
}

func scrub(v reflect.Value) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if !v.IsNil() {
			scrub(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			scrub(v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			scrub(v.Index(i))
		}
	case reflect.Map:
		// Map values are not addressable, so we set them back
		// after copying. Walk each value; if it's a float that
		// needs scrubbing, replace via SetMapIndex. For nested
		// structs inside the map, copy → scrub → re-set so the
		// inner mutation sticks.
		iter := v.MapRange()
		for iter.Next() {
			k := iter.Key()
			elem := iter.Value()
			// For map[string]any, elem.Kind() is Interface;
			// peel one layer to look at the concrete dynamic
			// type. If that's a float, scrub by writing back
			// a zero float into the slot.
			inner := elem
			if inner.Kind() == reflect.Interface && !inner.IsNil() {
				inner = inner.Elem()
			}
			switch inner.Kind() {
			case reflect.Float64, reflect.Float32:
				f := inner.Float()
				if math.IsNaN(f) || math.IsInf(f, 0) {
					if v.Type().Elem().Kind() == reflect.Interface {
						v.SetMapIndex(k, reflect.ValueOf(float64(0)))
					} else {
						v.SetMapIndex(k, reflect.Zero(v.Type().Elem()))
					}
				}
			case reflect.Ptr:
				if !inner.IsNil() {
					scrub(inner.Elem())
				}
			case reflect.Struct, reflect.Slice, reflect.Array, reflect.Map:
				// Copy out so the inner mutation has a settable
				// target, scrub, then re-set the slot. Required
				// because map values aren't directly addressable.
				copyVal := reflect.New(inner.Type()).Elem()
				copyVal.Set(inner)
				scrub(copyVal)
				// If the map's value type is `any`, wrap back as
				// interface{}; otherwise SetMapIndex matches type.
				if v.Type().Elem().Kind() == reflect.Interface {
					v.SetMapIndex(k, copyVal)
				} else {
					v.SetMapIndex(k, copyVal)
				}
			}
		}
	case reflect.Float64, reflect.Float32:
		if !v.CanSet() {
			return
		}
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			v.SetFloat(0)
		}
	}
}
