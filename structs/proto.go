/*
Copyright 2016 Tamás Gulácsi

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package structs

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	fstructs "github.com/fatih/structs"
	"github.com/pkg/errors"
)

//go:generate sh ./download-protoc.sh
//   go:generate go get -u github.com/golang/protobuf/protoc-gen-go
//go:generate go get -u github.com/gogo/protobuf/protoc-gen-gofast

// build: protoc --gofast_out=plugins=grpc:. my.proto

func SaveProtobuf(dst io.Writer, functions []Function, pkg string) error {
	var err error
	w := errWriter{Writer: dst, err: &err}

	io.WriteString(w, `syntax = "proto3";`+"\n\n")

	if pkg != "" {
		fmt.Fprintf(w, "package %s;\n", pkg)
	}
	io.WriteString(w, `
	import "github.com/gogo/protobuf/gogoproto/gogo.proto";
`)
	seen := make(map[string]struct{}, 16)

FunLoop:
	for _, fun := range functions {
		if err := fun.SaveProtobuf(w, seen); err != nil {
			if errors.Cause(err) == ErrMissingTableOf {
				Log("msg", "SKIP function, missing TableOf info", "function", fun.Name())
				continue FunLoop
			}
			return err
		}
		var streamQual string
		if fun.ReturnsCursor() {
			streamQual = "stream "
		}
		name := strings.ToLower(dot2D.Replace(fun.Name()))
		fmt.Fprintf(w, `
service %s {
	rpc %s (%s) returns (%s%s) {}
}
`, name,
			name, strings.ToLower(fun.getStructName(false)), streamQual, strings.ToLower(fun.getStructName(true)))
	}

	return nil
}

func (f Function) ReturnsCursor() bool {
	for _, arg := range f.Args {
		if arg.Direction&DIR_OUT != 0 && arg.Type == "REF CURSOR" {
			return true
		}
	}
	return false
}

func (f Function) SaveProtobuf(dst io.Writer, seen map[string]struct{}) error {
	var buf bytes.Buffer
	if err := f.saveProtobufDir(&buf, seen, false); err != nil {
		return errors.Wrap(err, "input")
	}
	if err := f.saveProtobufDir(&buf, seen, true); err != nil {
		return errors.Wrap(err, "output")
	}
	_, err := dst.Write(buf.Bytes())
	return err
}
func (f Function) saveProtobufDir(dst io.Writer, seen map[string]struct{}, out bool) error {
	dirmap, dirname := uint8(DIR_IN), "input"
	if out {
		dirmap, dirname = DIR_OUT, "output"
	}
	args := make([]Argument, 0, len(f.Args)+1)
	for _, arg := range f.Args {
		if arg.Direction&dirmap > 0 {
			args = append(args, arg)
		}
	}
	// return variable for function out structs
	if out && f.Returns != nil {
		args = append(args, *f.Returns)
	}

	return protoWriteMessageTyp(dst,
		dot2D.Replace(strings.ToLower(f.Name()))+"__"+dirname,
		seen, args...)
}

var dot2D = strings.NewReplacer(".", "__")

func protoWriteMessageTyp(dst io.Writer, msgName string, seen map[string]struct{}, args ...Argument) error {
	for _, arg := range args {
		if arg.Flavor == FLAVOR_TABLE && arg.TableOf == nil {
			return errors.Wrapf(ErrMissingTableOf, "no table of data for %s.%s (%v)", msgName, arg, arg)
		}
	}

	var err error
	w := errWriter{Writer: dst, err: &err}
	fmt.Fprintf(w, "\nmessage %s {\n", msgName)

	buf := buffers.Get()
	defer buffers.Put(buf)
	for i, arg := range args {
		var rule string
		if strings.HasSuffix(arg.Name, "#") {
			arg.Name = replHidden(arg.Name)
		}
		if arg.Flavor == FLAVOR_TABLE {
			if arg.TableOf == nil {
				return errors.Wrapf(ErrMissingTableOf, "no table of data for %s.%s (%v)", msgName, arg, arg)
			}
			rule = "repeated "
		}
		aName := arg.Name
		got := arg.goType(false)
		if arg.Type == "REF CURSOR" {
			Log("msg", "CUR", "got", got, "rule", rule, "arg", arg)
		}
		if strings.HasPrefix(got, "*") {
			got = got[1:]
		}
		if strings.HasPrefix(got, "[]") {
			rule = "repeated "
			got = got[2:]
		}
		if strings.HasPrefix(got, "*") {
			got = got[1:]
		}
		typ, pOpts := protoType(got)
		var optS string
		if s := pOpts.String(); s != "" {
			optS = " " + s
		}
		if arg.Flavor == FLAVOR_SIMPLE || arg.Flavor == FLAVOR_TABLE && arg.TableOf.Flavor == FLAVOR_SIMPLE {
			fmt.Fprintf(w, "\t%s%s %s = %d%s;\n", rule, typ, aName, i+1, optS)
			continue
		}
		if _, ok := seen[typ]; !ok {
			//lName := strings.ToLower(arg.Name)
			subArgs := make([]Argument, 0, 16)
			if arg.TableOf != nil {
				if arg.TableOf.RecordOf == nil {
					subArgs = append(subArgs, *arg.TableOf)
				} else {
					for _, v := range arg.TableOf.RecordOf {
						subArgs = append(subArgs, v)
					}
				}
			} else {
				for _, v := range arg.RecordOf {
					subArgs = append(subArgs, v)
				}
			}
			if err := protoWriteMessageTyp(buf, typ, seen, subArgs...); err != nil {
				Log("msg", "protoWriteMessageTyp", "error", err)
				return err
			}
			seen[typ] = struct{}{}
		}
		fmt.Fprintf(w, "\t%s%s %s = %d%s;\n", rule, typ, aName, i+1, optS)
	}
	io.WriteString(w, "}\n")
	w.Write(buf.Bytes())

	return err
}

func protoType(got string) (string, protoOptions) {
	switch trimmed := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(got, "[]"), "*")); trimmed {
	case "ora.time", "time.time":
		return "string", nil
	case "ora.string":
		return "string", nil
	case "int32":
		return "sint32", nil
	case "ora.int32":
		return "sint32", nil
	case "float64":
		return "double", nil
	case "ora.float64":
		return "double", nil

	case "ora.date", "custom.date":
		return "string", protoOptions(map[string]interface{}{
			"gogoproto.nullable":   false,
			"gogoproto.customtype": "github.com/tgulacsi/oracall/custom.Date",
		})
	case "n", "ora.n":
		return "string", protoOptions(map[string]interface{}{
			"gogoproto.nullable":   false,
			"gogoproto.customtype": "github.com/tgulacsi/oracall/custom.Number",
		})
	case "ora.lob":
		return "bytes", protoOptions(map[string]interface{}{
			"gogoproto.nullable":   false,
			"gogoproto.customtype": "github.com/tgulacsi/oracall/custom.Lob",
		})
	default:
		return trimmed, nil
	}
}

type protoOptions map[string]interface{}

func (opts protoOptions) String() string {
	if len(opts) == 0 {
		return ""
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for k, v := range opts {
		if buf.Len() != 1 {
			buf.WriteString(", ")
		}
		fmt.Fprintf(&buf, "(%s)=", k)
		switch v.(type) {
		case bool:
			fmt.Fprintf(&buf, "%t", v)
		default:
			fmt.Fprintf(&buf, "%q", v)
		}
	}
	buf.WriteByte(']')
	return buf.String()
}

func CopyStruct(dest interface{}, src interface{}) error {
	ds := fstructs.New(dest)
	ss := fstructs.New(src)
	snames := ss.Names()
	svalues := ss.Values()
	for _, df := range ds.Fields() {
		dnm := df.Name()
		for i, snm := range snames {
			if snm == dnm || dnm == goName(snm) || goName(dnm) == snm {
				svalue := svalues[i]
				if err := df.Set(svalue); err != nil {
					return errors.Wrapf(err, "set %q to %q (%v %T)", dnm, snm, svalue, svalue)
				}
			}
		}
	}
	return nil
}
