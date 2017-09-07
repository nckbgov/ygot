// Copyright 2017 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package ygen

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/openconfig/goyang/pkg/yang"
)

// protoMsgField describes a field of a protobuf message.
type protoMsgField struct {
	Tag        uint32            // Tag is the field number that should be used in the protobuf message.
	Name       string            // Name is the field's name.
	Type       string            // Type is the protobuf type for the field.
	IsRepeated bool              // IsRepeated indicates whether the field is repeated.
	Extensions map[string]string // Extensions is the set of field tags that are applied to the field.
}

// protoMsg describes a protobuf message.
type protoMsg struct {
	Name     string                   // Name is the name of the protobuf message to be output.
	YANGPath string                   // YANGPath stores the path that the message corresponds to within the YANG schema.
	Fields   []*protoMsgField         // Fields is a slice of the fields that are within the message.
	Imports  []string                 // Imports is a slice of strings that contains the relative import paths that are required by this message.
	Enums    map[string]*protoMsgEnum // Embedded enumerations within the message.
}

// protoMsgEnum represents an embedded enumeration within a protobuf message.
type protoMsgEnum struct {
	Values map[int64]string // The values that the enumerated type can take.
}

// protoEnum represents an enumeration that is defined at the root of a protobuf
// package.
type protoEnum struct {
	Name        string           // The name of the enum within the protobuf package.
	Description string           // The description of the enumerated type within the YANG schema, used in comments.
	Values      map[int64]string // The values that the enumerated type can take.
}

// proto3Header describes the header of a Protobuf3 package.
type proto3Header struct {
	PackageName            string   // PackageName is the name of the package that is to be output.
	BaseImportPath         string   // BaseImportPath specifies the path to generated protobufs that are to be imported by this protobuf, for example, the base repository URL in GitHub.
	Imports                []string // Imports is the set of packages that should be imported by the package whose header is being output.
	SourceYANGFiles        []string // SourceYANGFiles specifies the list of the input YANG files that the protobuf is being generated based on.
	SourceYANGIncludePaths []string // SourceYANGIncludePaths specifies the list of the paths that were used to search for YANG imports.
	CompressPaths          bool     // CompressPaths indicates whether path compression was enabled or disabled for this generated protobuf.
	CallerName             string   // CallerName indicates the name of the entity initiating code generation.
}

const (
	// defaultBasePackageName defines the default base package that is
	// generated when generating proto3 code.
	defaultBasePackageName = "openconfig"
	// defaultEnumPackageName defines the default package name that is
	// used for the package that defines enumerated types that are
	// used throughout the schema.
	defaultEnumPackageName = "enums"
)

var (
	// protoHeaderTemplate is populated and output at the top of the protobuf code output.
	protoHeaderTemplate = `
{{- /**/ -}}
// {{ .PackageName }} is generated by {{ .CallerName }} as a protobuf
// representation of a YANG schema.
//
// Input schema modules:
{{- range $inputFile := .SourceYANGFiles }}
//  - {{ $inputFile }}
{{- end }}
// Include paths:
{{- range $importPath := .SourceYANGIncludePaths }}
//   - {{ $importPath }}
{{- end }}
syntax = "proto3";

package {{ .PackageName }};

import "github.com/openconfig/ygot/proto/ywrapper/ywrapper.proto";
import "github.com/openconfig/ygot/proto/yext/yext.proto";
{{ $publicImport := .BaseImportPath -}}
{{- range $importedProto := .Imports }}
import "{{ filepathJoin $publicImport $importedProto }}.proto";
{{ end -}}
`

	// protoMessageTemplate is populated for each entity that is mapped to a message
	// within the output protobuf.
	protoMessageTemplate = `
// {{ .Name }} represents the {{ .YANGPath }} YANG schema element.
message {{ .Name }} {
{{- range $ename, $enum := .Enums }}
  enum {{ $ename }} {
    {{- range $i, $val := $enum.Values }}
    {{ $ename }}_{{ $val }} = {{ $i }};
    {{- end }}
  }
{{- end -}}
{{- range $idx, $field := .Fields }}
  {{ if $field.IsRepeated }}repeated {{ end -}}
  {{ $field.Type }} {{ $field.Name }} = {{ $field.Tag }}
  {{- $noExtensions := len .Extensions -}}
  {{- if ne $noExtensions 0 -}} [
    {{- range $i, $opt := $field.Extensions -}}
      {{- $opt -}}
      {{- if ne (inc $i) $noExtensions -}}, {{- end }}
   {{- end -}}
  ]
  {{- end -}}
  ;
{{- end }}
}
`

	// protoListKeyTemplate is generated as a wrapper around each list entry within
	// a YANG schema that has a key.
	protoListKeyTemplate = `
// {{ .Name }} represents the list element {{ .YANGPath }} of the YANG schema. It
// contains only the keys of the list, and an embedded message containing all entries
// below this entity in the schema.
message {{ .Name }} {
{{- range $idx, $field := .Fields }}
  {{ $field.Type }} {{ $field.Name }} = {{ $field.Tag }}
{{- end }}
}
`

	// protoEnumTemplate is the template used to generate enumerations that are
	// not within a message. Such enums are used where there are referenced YANG
	// identity nodes, and where there are typedefs which include an enumeration.
	protoEnumTemplate = `
// {{ .Name }} represents an enumerated type generated for the {{ .Description }}.
enum {{ .Name }} {
{{- range $i, $val := .Values }}
  {{ $.Name }}_{{ $val }} = {{ $i }};
{{- end }}
}
`

	// protoTemplates is the set of templates that are referenced during protbuf
	// code generation.
	protoTemplates = map[string]*template.Template{
		"header": makeTemplate("header", protoHeaderTemplate),
		"msg":    makeTemplate("msg", protoMessageTemplate),
		"list":   makeTemplate("list", protoListKeyTemplate),
		"enum":   makeTemplate("enum", protoEnumTemplate),
	}
)

// writeProto3Header outputs the header for a proto3 generated file. It takes
// an input proto3Header struct specifying the input arguments describing the
// generated package, and returns a string containing the generated package's
// header.
func writeProto3Header(in proto3Header) (string, error) {
	if in.CallerName == "" {
		in.CallerName = callerName()
	}

	var b bytes.Buffer
	if err := protoTemplates["header"].Execute(&b, in); err != nil {
		return "", err
	}

	return b.String(), nil
}

// writeProto3Msg generates a protobuf message for the *yangDirectory described by msg.
// it uses the context of other messages to be generated (msgs), and the generator state
// stored in state to determine names of other messages.  compressPaths indicates whether
// path compression should be enabled for the code generation. The basePackageName
// supplied specifies the base package name that is used or the generated protobufs, and
// the enumPackageName specifies the name of the package which enumerated type defintiions
// are written to. Returns a string containing the name of the package that the message is
// within, a string containing the generated code for the protobuf message, a slice of
// strings containing the child packages that are required by this message and any errors
// encountered during proto generation.
func writeProto3Msg(msg *yangDirectory, msgs map[string]*yangDirectory, state *genState, compressPaths bool, basePackageName, enumPackageName string) (string, string, []string, []error) {
	msgDefs, errs := genProto3Msg(msg, msgs, state, compressPaths, basePackageName, enumPackageName)
	if len(errs) > 0 {
		return "", "", nil, errs
	}

	if msg.entry.Parent == nil {
		return "", "", nil, []error{fmt.Errorf("YANG schema element %s does not have a parent, protobuf messages are not generated for modules", msg.entry.Path())}
	}

	// pkg is the name of the protobuf package, if the entry's parent has already
	// been seen in the schema, the same package name as for siblings of this
	// entry will be returned.
	pkg := state.protobufPackage(msg.entry, compressPaths)

	var b bytes.Buffer
	imports := []string{}
	for _, msgDef := range msgDefs {
		if err := protoTemplates["msg"].Execute(&b, msgDef); err != nil {
			return "", "", nil, []error{err}
		}
		imports = appendEntriesNotIn(imports, msgDef.Imports)
	}

	return pkg, b.String(), imports, nil

}

// genProto3Msg takes an input yangDirectory which describes a container or list entry
// within the YANG schema and returns a protoMsg which can be mapped to the protobuf
// code representing it. It uses the set of messages that have been extracted and the
// current generator state to map to other messages and ensure uniqueness of names.
// The configuration parameters for the current code generation required are supplied
// as arguments, particularly whether path is compression is enabled, the base package
// name and the name of the package that enumerated types are written to.
func genProto3Msg(msg *yangDirectory, msgs map[string]*yangDirectory, state *genState, compressPaths bool, basePackageName, enumPackageName string) ([]protoMsg, []error) {
	var errs []error

	var msgDefs []protoMsg

	msgDef := protoMsg{
		// msg.name is already specified to be CamelCase in the form we expect it
		// to be for the protobuf message name.
		Name:     msg.name,
		YANGPath: slicePathToString(msg.path),
		Enums:    make(map[string]*protoMsgEnum),
	}

	definedFieldNames := map[string]bool{}
	imports := []string{}

	// Traverse the fields in alphabetical order to ensure deterministic output.
	// TODO(robjs): Once the field tags are unique then make this sort on the
	// field tag.
	fNames := []string{}
	for name := range msg.fields {
		fNames = append(fNames, name)
	}
	sort.Strings(fNames)

	// If the message that we are generating for is a list, then we explicitly
	// want to skip it keys to ensure that they are not duplicated.
	skipFields := map[string]bool{}
	if msg.entry.IsList() && msg.entry.Key != "" {
		for _, k := range strings.Split(msg.entry.Key, " ") {
			skipFields[k] = true
		}
	}

	for _, name := range fNames {
		// Skip fields that we are explicitly not asked to include.
		if _, ok := skipFields[name]; ok {
			continue
		}

		field := msg.fields[name]

		fieldDef := &protoMsgField{
			Name: makeNameUnique(safeProtoFieldName(name), definedFieldNames),
		}

		t, err := protoTagForEntry(field)
		if err != nil {
			errs = append(errs, fmt.Errorf("proto: could not generate tag for field %s: %v", field.Name, err))
			continue
		}
		fieldDef.Tag = t

		switch {
		case field.IsList():
			listMsg, ok := msgs[field.Path()]
			if !ok {
				errs = append(errs, fmt.Errorf("proto: could not resolve list %s into a defined message", field.Path()))
				continue
			}

			listMsgName, ok := state.uniqueDirectoryNames[field.Path()]
			if !ok {
				errs = append(errs, fmt.Errorf("proto: could not find unique message name for %s", field.Path()))
				continue
			}

			childPkg := state.protobufPackage(listMsg.entry, compressPaths)

			switch {
			case listMsg.listAttr == nil, len(listMsg.listAttr.keys) == 0:
				// This is a list that does not have a key within the YANG schema. In
				// Proto3 we represent this as a repeated field of the parent message.
				fieldDef.Type = fmt.Sprintf("%s.%s", childPkg, listMsgName)
			default:
				// In the case that we have a keyed list, then we represent it as a
				// repeated message hiearchy such that:
				//
				// list foo {
				//	key "bar";
				//	leaf bar { type string; }
				//	leaf value { type uint32; }
				// }
				//
				// Is mapped to a "repeated pkg.FooList foo = NN;" in the parent message
				// and a new Foo message:
				//
				// message Foo {
				//	string bar = NN;
				//	FooListEntry value = NN;
				// }
				//
				// message FooListEntry {
				//	ywrapper.UintValue value = 1;
				// }
				//
				// In the case that there is >1 key, then each key that exists for the
				// foo list is contained within the Foo message. This ensures that there
				// is consistency for the all different types of maps, and different
				// types of keys (e.g., those that are enumerations).

				// listKeyMsg is the newly created message that is the interim layer
				// between this message and the entry that will have code specifically
				// generated for it (skipping the key fields).
				listKeyMsg, err := genListKeyProto(listMsg, listMsgName, childPkg, state)
				if err != nil {
					errs = append(errs, fmt.Errorf("proto: could not build mapping for list entry %s: %v", field.Path(), err))
					continue
				}
				// The type of this field is just the key message's name, since it
				// will be in the same package as the field's parent.
				fieldDef.Type = listKeyMsg.Name
				msgDefs = append(msgDefs, listKeyMsg)
			}
			fieldDef.IsRepeated = true
		case field.IsDir():
			childmsg, ok := msgs[field.Path()]
			if !ok {
				err = fmt.Errorf("proto: could not resolve %s into a defined struct", field.Path())
			} else {

				childpkg := state.protobufPackage(childmsg.entry, compressPaths)

				// Add the import to the slice of imports if it is not already
				// there. This allows the message file to import the required
				// child packages.
				childpath := strings.Replace(childpkg, ".", "/", -1)
				imports = appendEntriesNotIn(imports, []string{childpath})
				fieldDef.Type = fmt.Sprintf("%s.%s", childpkg, childmsg.name)
			}
		case field.IsLeaf() || field.IsLeafList():
			var protoType mappedType
			protoType, err = state.yangTypeToProtoType(resolveTypeArgs{yangType: field.Type, contextEntry: field}, basePackageName, enumPackageName)
			switch {
			case field.Type.Kind == yang.Yenum && field.Type.Name == "enumeration":
				// For fields that are enumerations, then we embed an enum within
				// the Protobuf message. Check for the type of the name to ensure
				// that this is a simple enumeration leaf, not a typedef.
				enum, eerr := genProtoEnum(field)
				if eerr != nil {
					err = eerr
				} else {
					e := makeNameUnique(protoType.nativeType, definedFieldNames)
					msgDef.Enums[e] = enum
					fieldDef.Type = e
				}
			case field.Type.Kind == yang.Yenum, field.Type.Kind == yang.Yidentityref:
				// When we have an enumeration that is a typedef, or an identityref then we need
				// to reference the enumerated package.
				imports = appendEntriesNotIn(imports, []string{fmt.Sprintf("%s/%s", basePackageName, enumPackageName)})
				fallthrough
			default:
				fieldDef.Type = protoType.nativeType
			}

			if field.ListAttr != nil {
				fieldDef.IsRepeated = true
			}
		default:
			err = fmt.Errorf("proto: unknown field type in message %s, field %s", msg.name, field.Name)
		}

		if err != nil {
			errs = append(errs, err)
			continue
		}
		msgDef.Fields = append(msgDef.Fields, fieldDef)
	}

	// Append the deduplicated imports to the list of imports required for the
	// message.
	msgDef.Imports = imports

	return append(msgDefs, msgDef), errs
}

// writeProtoEnums takes a map of enumerations, described as yangEnum structs, and returns
// the mapped protobuf enum definition that is required. It skips any identified enumerated
// type that is a simple enumerated leaf, as these are output as embedded enumerations within
// each message. It returns a slice of srings containing the generated code, and the slice
// of errors experienced.
func writeProtoEnums(enums map[string]*yangEnum) ([]string, []error) {
	var errs []error
	var genEnums []string
	for _, enum := range enums {
		if enum.entry.Type.Kind == yang.Yenum && enum.entry.Type.Name == "enumeration" {
			// Skip simple enumerations.
			continue
		}

		p := &protoEnum{Name: enum.name}
		switch {
		case enum.entry.Type.IdentityBase != nil:
			// This input enumeration is an identityref leaf. The values are based on
			// the name of the identities that correspond with the base, and the value
			// is gleaned from the YANG schema.
			values := map[int64]string{0: "UNSET"}

			// TODO(robjs): Implement a consistent approach for enumeration values.
			// This approach will cause issues when there is an entry added which
			// causes an entry earlier in the sequence than others.
			names := []string{}
			for _, v := range enum.entry.Type.IdentityBase.Values {
				names = append(names, safeProtoEnumName(v.Name))
			}
			sort.Strings(names)

			for i, n := range names {
				values[int64(i)+1] = n
			}
			p.Values = values
			p.Description = fmt.Sprintf("YANG identity %s", enum.entry.Type.IdentityBase.Name)
		case enum.entry.Type.Kind == yang.Yenum:
			ge, err := genProtoEnum(enum.entry)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			p.Values = ge.Values
			p.Description = fmt.Sprintf("YANG typedef %s", enum.entry.Type.Name)
		case len(enum.entry.Type.Type) != 0:
			errs = append(errs, fmt.Errorf("unimplemented: support for multiple enumerations within a union for %v", enum.name))
			continue
		default:
			errs = append(errs, fmt.Errorf("unknown type of enumerated value in writeProtoEnums for %s, got: %v", enum.name, enum))
		}

		var b bytes.Buffer
		if err := protoTemplates["enum"].Execute(&b, p); err != nil {
			errs = append(errs, fmt.Errorf("cannot generate enumeration for %s: %v", enum.name, err))
			continue
		}
		genEnums = append(genEnums, b.String())
	}

	if len(errs) != 0 {
		return nil, errs
	}
	return genEnums, nil
}

// genProtoEnum takes an input yang.Entry that contains an enumerated type
// and returns a protoMsgEnum that contains its definition within the proto
// schema.
func genProtoEnum(field *yang.Entry) (*protoMsgEnum, error) {
	eval := map[int64]string{}
	names := field.Type.Enum.NameMap()
	eval[0] = "UNSET"
	if d := field.DefaultValue(); d != "" {
		if _, ok := names[d]; !ok {
			return nil, fmt.Errorf("enumeration %s specified a default - %s - that was not a valid value", field.Path(), d)
		}
		eval[0] = d
	}

	for n := range names {
		if n == field.DefaultValue() {
			// Can't happen if there was not a default, since "" is not
			// a valid enumeration name in YANG.
			continue
		}
		// We always add one to the value that is returned to ensure that
		// we never redefine value 0.
		eval[field.Type.Enum.Value(n)+1] = safeProtoEnumName(n)
	}

	// TODO(robjs): Embed an option into the message such that we can persist
	// the eval map -- this would allow a consumer to be able to map back to the
	// string that is in the YANG schema.
	return &protoMsgEnum{Values: eval}, nil
}

// safeProtoFieldName takes an input string which represents the name of a YANG schema
// element and sanitises for use as a protobuf field name.
func safeProtoFieldName(name string) string {
	// YANG identifiers must match the definition:
	//    ;; An identifier MUST NOT start with (('X'|'x') ('M'|'m') ('L'|'l'))
	//       identifier          = (ALPHA / "_")
	//                                *(ALPHA / DIGIT / "_" / "-" / ".")
	// For Protobuf they must match:
	//	ident = letter { letter | decimalDigit | "_" }
	//
	// Therefore we need to ensure that the "-", and "." characters that are allowed
	// in the YANG are replaced.
	replacer := strings.NewReplacer(
		".", "_",
		"-", "_",
	)
	return replacer.Replace(name)
}

// safeProtoEnumName takes na input string which represents the name of a YANG enumeration
// value, or identity name, and sanitises it for use in a protobuf schema.
func safeProtoEnumName(name string) string {
	replacer := strings.NewReplacer(
		".", "_",
		"*", "_",
		"/", "_",
	)
	return replacer.Replace(name)
}

// fieldTag returns a protobuf tag value for the entry e. The tag value supplied is
// between 1 and 2^29-1. The values 19,000-19,999 are excluded as these are explicitly
// reserved for protobuf-internal use by https://developers.google.com/protocol-buffers/docs/proto3.
func protoTagForEntry(e *yang.Entry) (uint32, error) {
	// TODO(robjs): Replace this function with the final implementation
	// once concluded.
	return 1, nil
}

// genListKeyProto generates a protoMsg that describes the proto3 message that represents
// the key of a list for YANG lists. It takes a yangDirectory pointer to the list being
// described, the name of the list, the package name that the list is within, and the
// current generator state. Returns the definition of the list key proto.
func genListKeyProto(list *yangDirectory, listName string, listPackage string, state *genState) (protoMsg, error) {
	// TODO(robjs): Check whether we need to make sure that this is unique.
	n := fmt.Sprintf("%s_Key", listName)
	km := protoMsg{
		Name:     n,
		YANGPath: list.entry.Path(),
		Enums:    map[string]*protoMsgEnum{},
	}

	definedFieldNames := map[string]bool{}
	ctag := uint32(1)
	for _, k := range strings.Split(list.entry.Key, " ") {
		kf, ok := list.fields[k]
		if !ok {
			return protoMsg{}, fmt.Errorf("list %s included a key %s did that did not exist", list.entry.Path(), k)
		}

		t, err := state.yangTypeToProtoScalarType(resolveTypeArgs{yangType: kf.Type, contextEntry: kf})
		if err != nil {
			return protoMsg{}, fmt.Errorf("list %s included a key %s that did not have a valid proto type: %v", list.entry.Path(), k, kf.Type)
		}

		var pt string
		switch {
		case kf.Type.Kind == yang.Yenum && kf.Type.Name == "enumeration":
			enum, err := genProtoEnum(kf)
			if err != nil {
				return protoMsg{}, fmt.Errorf("error generating type for list key %s, type %s", list.entry.Path(), k, kf.Type)
			}
			pt = makeNameUnique(t.nativeType, definedFieldNames)
			km.Enums[pt] = enum
		default:
			pt = t.nativeType
		}

		km.Fields = append(km.Fields, &protoMsgField{
			Name: makeNameUnique(safeProtoFieldName(k), definedFieldNames),
			Tag:  ctag,
			Type: pt,
		})

		ctag++
	}

	km.Fields = append(km.Fields, &protoMsgField{
		Name: listName,
		Type: fmt.Sprintf("%s.%s", listPackage, listName),
		Tag:  ctag,
	})

	return km, nil
}
