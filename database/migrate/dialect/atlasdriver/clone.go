package atlasdriver

import (
	"reflect"

	"ariga.io/atlas/sql/schema"
)

// cloneSchema deep-copies an evaluated schema, keeping its pointer graph: a
// pointer reached twice (a table's schema, an index part's column, a foreign
// key's referenced table, an enum a column uses) is copied once and shared
// again in the copy, and cycles close on the copy. Exported fields are copied
// recursively; unexported ones are copied by value, as the struct holding them.
func cloneSchema(s *schema.Schema) *schema.Schema {
	c := cloner{}
	return c.copy(reflect.ValueOf(s)).Interface().(*schema.Schema)
}

type cloneKey struct {
	t reflect.Type
	p uintptr
}

type cloner map[cloneKey]reflect.Value

func (c cloner) copy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		return c.copyPointer(v)
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(c.copy(v.Elem()))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(c.copy(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			out.SetMapIndex(c.copy(it.Key()), c.copy(it.Value()))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := range v.NumField() {
			if out.Field(i).CanSet() {
				out.Field(i).Set(c.copy(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}

func (c cloner) copyPointer(v reflect.Value) reflect.Value {
	if v.IsNil() {
		return v
	}
	key := cloneKey{v.Type(), v.Pointer()}
	if out, ok := c[key]; ok {
		return out
	}
	out := reflect.New(v.Type().Elem())
	c[key] = out
	out.Elem().Set(c.copy(v.Elem()))
	return out
}
