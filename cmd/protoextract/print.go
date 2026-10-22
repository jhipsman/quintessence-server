package main

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/descriptorpb"
)

// PrintProto writes a FileDescriptorProto back as .proto source: syntax,
// package, imports, messages (with nested types, oneofs and maps), enums and
// services. Options other than map entries and defaults are left out.
func PrintProto(f *descriptorpb.FileDescriptorProto) string {
	p := &printer{proto3: f.GetSyntax() == "proto3"}
	syntax := f.GetSyntax()
	if syntax == "" {
		syntax = "proto2"
	}
	p.line(0, "// %s, recovered from its compiled descriptor", f.GetName())
	p.line(0, "syntax = %q;", syntax)
	if f.GetPackage() != "" {
		p.line(0, "")
		p.line(0, "package %s;", f.GetPackage())
	}
	if len(f.GetDependency()) > 0 {
		p.line(0, "")
		for _, d := range f.GetDependency() {
			p.line(0, "import %q;", d)
		}
	}
	for _, e := range f.GetEnumType() {
		p.line(0, "")
		p.enum(0, e)
	}
	for _, m := range f.GetMessageType() {
		p.line(0, "")
		p.message(0, m)
	}
	for _, s := range f.GetService() {
		p.line(0, "")
		p.service(s)
	}
	if len(f.GetExtension()) > 0 {
		p.line(0, "")
		p.extensions(0, f.GetExtension())
	}
	return p.b.String()
}

type printer struct {
	b      strings.Builder
	proto3 bool
}

func (p *printer) line(depth int, format string, args ...any) {
	if format == "" {
		p.b.WriteString("\n")
		return
	}
	p.b.WriteString(strings.Repeat("  ", depth))
	fmt.Fprintf(&p.b, format, args...)
	p.b.WriteString("\n")
}

func (p *printer) enum(depth int, e *descriptorpb.EnumDescriptorProto) {
	p.line(depth, "enum %s {", e.GetName())
	for _, v := range e.GetValue() {
		p.line(depth+1, "%s = %d;", v.GetName(), v.GetNumber())
	}
	p.line(depth, "}")
}

func (p *printer) message(depth int, m *descriptorpb.DescriptorProto) {
	p.line(depth, "message %s {", m.GetName())
	maps := map[string]*descriptorpb.DescriptorProto{}
	for _, n := range m.GetNestedType() {
		if n.GetOptions().GetMapEntry() {
			maps[n.GetName()] = n
		}
	}
	for _, e := range m.GetEnumType() {
		p.enum(depth+1, e)
	}
	for _, n := range m.GetNestedType() {
		if !n.GetOptions().GetMapEntry() {
			p.message(depth+1, n)
		}
	}

	// Fields in their declared order; the ones of a real oneof are printed
	// together in its block, where its first field appears.
	printedOneof := map[int32]bool{}
	for _, fl := range m.GetField() {
		if fl.OneofIndex != nil && !fl.GetProto3Optional() {
			idx := fl.GetOneofIndex()
			if printedOneof[idx] {
				continue
			}
			printedOneof[idx] = true
			p.line(depth+1, "oneof %s {", m.GetOneofDecl()[idx].GetName())
			for _, of := range m.GetField() {
				if of.OneofIndex != nil && of.GetOneofIndex() == idx && !of.GetProto3Optional() {
					p.field(depth+2, of, maps, true)
				}
			}
			p.line(depth+1, "}")
			continue
		}
		p.field(depth+1, fl, maps, false)
	}
	if len(m.GetExtension()) > 0 {
		p.extensions(depth+1, m.GetExtension())
	}
	for _, r := range m.GetReservedRange() {
		if r.GetEnd()-1 == r.GetStart() {
			p.line(depth+1, "reserved %d;", r.GetStart())
		} else {
			p.line(depth+1, "reserved %d to %d;", r.GetStart(), r.GetEnd()-1)
		}
	}
	p.line(depth, "}")
}

func (p *printer) field(depth int, f *descriptorpb.FieldDescriptorProto, maps map[string]*descriptorpb.DescriptorProto, inOneof bool) {
	typ := fieldType(f)
	if entry, ok := maps[lastPart(f.GetTypeName())]; ok && f.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
		var k, v string
		for _, ef := range entry.GetField() {
			if ef.GetNumber() == 1 {
				k = fieldType(ef)
			} else if ef.GetNumber() == 2 {
				v = fieldType(ef)
			}
		}
		p.line(depth, "map<%s, %s> %s = %d;", k, v, f.GetName(), f.GetNumber())
		return
	}
	label := ""
	switch {
	case inOneof:
	case f.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED:
		label = "repeated "
	case f.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REQUIRED:
		label = "required "
	case !p.proto3 || f.GetProto3Optional():
		label = "optional "
	}
	opts := ""
	if f.DefaultValue != nil {
		dv := f.GetDefaultValue()
		if f.GetType() == descriptorpb.FieldDescriptorProto_TYPE_STRING || f.GetType() == descriptorpb.FieldDescriptorProto_TYPE_BYTES {
			dv = fmt.Sprintf("%q", dv)
		}
		opts = fmt.Sprintf(" [default = %s]", dv)
	}
	p.line(depth, "%s%s %s = %d%s;", label, typ, f.GetName(), f.GetNumber(), opts)
}

func (p *printer) extensions(depth int, exts []*descriptorpb.FieldDescriptorProto) {
	byExtendee := map[string][]*descriptorpb.FieldDescriptorProto{}
	var order []string
	for _, e := range exts {
		if _, ok := byExtendee[e.GetExtendee()]; !ok {
			order = append(order, e.GetExtendee())
		}
		byExtendee[e.GetExtendee()] = append(byExtendee[e.GetExtendee()], e)
	}
	for _, ext := range order {
		p.line(depth, "extend %s {", ext)
		for _, e := range byExtendee[ext] {
			p.field(depth+1, e, nil, false)
		}
		p.line(depth, "}")
	}
}

func (p *printer) service(s *descriptorpb.ServiceDescriptorProto) {
	p.line(0, "service %s {", s.GetName())
	for _, m := range s.GetMethod() {
		in, out := m.GetInputType(), m.GetOutputType()
		if m.GetClientStreaming() {
			in = "stream " + in
		}
		if m.GetServerStreaming() {
			out = "stream " + out
		}
		p.line(1, "rpc %s(%s) returns (%s);", m.GetName(), in, out)
	}
	p.line(0, "}")
}

// fieldType is the type as written in .proto: a scalar keyword, or the
// fully qualified message/enum name (with its leading dot, which protoc accepts).
func fieldType(f *descriptorpb.FieldDescriptorProto) string {
	if f.GetTypeName() != "" {
		return f.GetTypeName()
	}
	name := strings.ToLower(strings.TrimPrefix(f.GetType().String(), "TYPE_"))
	return name
}

func lastPart(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}
