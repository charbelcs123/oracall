/*
Copyright 2015 Tamás Gulácsi

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
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var flagKeep = flag.Bool("keep", false, "keep temp files")

func TestWriteStruct(t *testing.T) {
	var (
		dn, fn string
		keep   = *flagKeep
		err    error
	)
	for i, tc := range testCases {
		functions := tc.ParseCsv(t, i)

		if dn == "" {
			if dn, err = ioutil.TempDir("", "structs-"); err != nil {
				t.Skipf("cannot create temp dir: %v", err)
				return
			}
			defer func() {
				if !keep {
					os.RemoveAll(dn)
				}
			}()
		}
		if !keep && fn != "" {
			_ = os.Remove(fn)
		}
		fn = filepath.Join(dn, fmt.Sprintf("main-%d.go", i))
		defer func() {
			if !keep {
				os.Remove(fn)
			}
		}()
		fh, err := os.Create(fn)
		if err != nil {
			t.Skipf("cannot create temp file in %q: %v", dn, err)
			return
		}
		err = SaveFunctions(fh, functions, "main", false)
		if err != nil {
			_ = fh.Close()
			t.Errorf("%d. Saving functions: %v", i, err)
			t.FailNow()
		}
		if _, err = io.WriteString(fh, "\nfunc main() {}\n"); err != nil {
			t.Errorf("%d. append main: %v", i, err)
		}
		if err = fh.Close(); err != nil {
			t.Errorf("%d. Writing to %s: %v", i, fh.Name(), err)
		}
		cmd := exec.Command("go", "run", fh.Name())
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			keep = true
			t.Errorf("%d. go run %q: %v", i, fh.Name(), err)
			t.FailNow()
		}
	}
}
