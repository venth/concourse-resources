// Copyright 2017 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/build/gerrit"

	"github.com/google/concourse-resources/internal"
)

const (
	testProject        = "testproject"
	testBranch         = "testbranch"
	testSubject        = "Test Subject"
	testChangeIdPrefix = "Itestchange"
	testRevisionPrefix = "deadbeef"
	testName           = "Testy McTestface"
	testEmail          = "testy@example.com"
)

var (
	testTempDir string

	testGerritUrl string

	testGerritLastAuthenticated bool
	testGerritLastRequest       *http.Request
	testGerritLastQ             string
	testGerritLastN             int
	testGerritLastChangeId      string
	testGerritLastRevision      string
	testGerritLastReviewInput   *gerrit.ReviewInput
)

type testRequest struct {
	Source  `json:"source"`
	Version `json:"version"`
	Params  interface{} `json:"params"`
}

type testResourceResponse struct {
	Version `json:"version"`
	Metadata []internal.MetadataField `json:"metadata"`
}

func TestMain(m *testing.M) {
	var err error
	testTempDir, err = ioutil.TempDir("", "concourse-gerrit-test")
	if err != nil {
		panic(err)
	}
	cookiesTempDir = testTempDir
	updateStampTempDir = testTempDir

	testServer := httptest.NewServer(http.HandlerFunc(testGerritHandler))
	testGerritUrl = testServer.URL

	// Replace IP in test URL with "localhost" (if equivalent) so cookies work.
	localhostIp, err := net.ResolveIPAddr("ip4", "localhost")
	if err == nil && strings.Contains(testGerritUrl, localhostIp.String()) {
		testGerritUrl = strings.Replace(
			testGerritUrl, localhostIp.String(), "localhost", 1)
	}

	// Mock out git execution
	execGit = testExecGit

	ret := m.Run()

	testServer.Close()
	os.RemoveAll(testTempDir)
	os.Exit(ret)
}

func testExecGit(_ ...string) ([]byte, error) {
	return []byte{}, nil
}

func testBuildChange(testNumber int, revisionCount int) gerrit.ChangeInfo {
	changeId := fmt.Sprintf("%s%d", testChangeIdPrefix, testNumber)
	change := gerrit.ChangeInfo{
		ID:           fmt.Sprintf("%s~%s~%s", testProject, testBranch, changeId),
		ChangeNumber: testNumber,
		Project:      testProject,
		Branch:       testBranch,
		ChangeID:     changeId,
		Subject:      testSubject,
		Revisions:    make(map[string]gerrit.RevisionInfo),
	}
	for i := 0; i < revisionCount; i++ {
		revision := fmt.Sprintf("%s%d", testRevisionPrefix, i)
		patchSetNumber := i + 1
		created := gerrit.TimeStamp(time.Unix(int64(100*testNumber+10000*i), 0))
		ref := fmt.Sprintf("refs/changes/1/%d/%d", testNumber, i+1)
		change.Revisions[revision] = gerrit.RevisionInfo{
			PatchSetNumber: patchSetNumber,
			Created:        created,
			Uploader: &gerrit.AccountInfo{
				Name:  testName,
				Email: testEmail,
			},
			Ref: ref,
			Fetch: map[string]*gerrit.FetchInfo{
				"http": &gerrit.FetchInfo{
					URL: fmt.Sprintf("%s/%s.git", testGerritUrl, testProject),
					Ref: ref,
				},
				"fake": &gerrit.FetchInfo{
					URL: "fake://example.com",
					Ref: "fake/ref",
				},
			},
		}
		change.CurrentRevision = revision
		change.Updated = created
	}
	return change
}

func testGerritWriteResponse(w http.ResponseWriter, v interface{}) {
	// The gerrit client expects a XSRF-defeating header first
	_, err := w.Write([]byte(")]}'\n"))
	if err != nil {
		panic(err)
	}
	err = json.NewEncoder(w).Encode(v)
	if err != nil {
		panic(err)
	}
}

func testGerritHandler(w http.ResponseWriter, r *http.Request) {
	testGerritLastRequest = r

	revisionCount := 0
	for _, o := range r.URL.Query()["o"] {
		switch o {
		case "CURRENT_REVISION":
			revisionCount = 1
		case "ALL_REVISIONS":
			revisionCount = 3
		}
	}

	var err error

	testGerritLastAuthenticated = strings.HasPrefix(r.URL.Path, "/a")
	path := strings.TrimPrefix(r.URL.Path, "/a")
	pathParts := strings.Split(path, "/")

	if path == "/changes/" {
		testGerritLastQ = r.URL.Query().Get("q")
		testGerritLastN, _ = strconv.Atoi(r.URL.Query().Get("n"))

		if testGerritLastQ == "" {
			panic("no q param for /changes/")
		}

		n := testGerritLastN
		if n == 0 {
			n = 3
		}

		var changes []gerrit.ChangeInfo
		for i := 0; i < n; i++ {
			changes = append(changes, testBuildChange(i+1, revisionCount))
		}
		// Sort changes by update time descending
		sort.Slice(changes, func(i, j int) bool {
			return changes[i].Updated.Time().After(changes[j].Updated.Time())
		})
		testGerritWriteResponse(w, changes)
	} else if strings.HasSuffix(path, "/review") {
		testGerritLastChangeId = pathParts[2]
		testGerritLastRevision = pathParts[4]
		err = json.NewDecoder(r.Body).Decode(&testGerritLastReviewInput)
		if err != nil {
			panic(err)
		}
		// The gerrit client seems to ignore this response
		testGerritWriteResponse(w, map[string]string{})
	} else if strings.HasPrefix(path, "/changes/") {
		testGerritLastChangeId = pathParts[2]
		if strings.HasPrefix(testGerritLastChangeId, testChangeIdPrefix) {
			testNumber, _ := strconv.Atoi(
				strings.TrimPrefix(testGerritLastChangeId, testChangeIdPrefix))
			testGerritWriteResponse(w, testBuildChange(testNumber, revisionCount))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	} else {
		panic("Unhandled path " + path)
	}
	if err != nil {
		panic(err)
	}
}

func testJsonReader(v interface{}) *json.Decoder {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return json.NewDecoder(bytes.NewBuffer(data))
}
