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
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"

	"golang.org/x/build/gerrit"

	"github.com/google/concourse-resources/internal"
)

const (
	gerritVersionFilename = ".gerrit_version.json"
)

var (
	defaultFetchProtocols = []string{"http", "anonymous http"}

	// For testing
	execGit = realExecGit
)

type inParams struct {
	FetchProtocol string `json:"fetch_protocol"`
	FetchUrl      string `json:"fetch_url"`
}

func init() {
	internal.RegisterInFunc(in)
}

func in(req internal.InRequest) error {
	var src Source
	var ver Version
	var params inParams
	err := req.Decode(&src, &ver, &params)
	if err != nil {
		return err
	}

	authMan := newAuthManager(src)
	defer authMan.cleanup()

	c, err := gerritClient(src, authMan)
	if err != nil {
		return fmt.Errorf("error setting up gerrit client: %v", err)
	}

	ctx := context.Background()

	// Fetch requested version from Gerrit
	change, rev, err := getVersionChangeRevision(c, ctx, ver)
	if err != nil {
		return err
	}

	fetchArgs, err := resolveFetchArgs(params, rev)
	if err != nil {
		return fmt.Errorf("could not resolve fetch args for change %q: %v", change.ID, err)
	}

	// Prepare destination repo and checkout requested revision
	err = git(req.TargetDir(), "init")
	if err != nil {
		return err
	}
	err = git(req.TargetDir(), "config", "color.ui", "always")
	if err != nil {
		return err
	}

	configArgs, err := authMan.gitConfigArgs()
	if err != nil {
		return fmt.Errorf("error getting git config args: %v", err)
	}
	err = git(req.TargetDir(), configArgs...)
	if err != nil {
		return err
	}

	err = git(req.TargetDir(), fetchArgs...)
	if err != nil {
		return err
	}
	err = git(req.TargetDir(), "checkout", "FETCH_HEAD")
	if err != nil {
		return err
	}

	// Build response metadata
	req.AddResponseMetadata("project", change.Project)
	req.AddResponseMetadata("subject", change.Subject)
	if rev.Uploader != nil {
		req.AddResponseMetadata("uploader", fmt.Sprintf("%s <%s>", rev.Uploader.Name, rev.Uploader.Email))
	}
	link, err := buildRevisionLink(src, change.ChangeNumber, rev.PatchSetNumber)
	if err == nil {
		req.AddResponseMetadata("link", link)
	} else {
		log.Printf("error building revision link: %v", err)
	}

	// Write gerrit_version.json
	gerritVersionPath := filepath.Join(req.TargetDir(), gerritVersionFilename)
	err = ver.WriteToFile(gerritVersionPath)
	if err != nil {
		return fmt.Errorf("error writing %q: %v", gerritVersionPath, err)
	}

	// Ignore gerrit_version.json file in repo
	excludePath := filepath.Join(req.TargetDir(), ".git", "info", "exclude")
	excludeErr := os.MkdirAll(filepath.Dir(excludePath), 0755)
	if excludeErr == nil {
		f, excludeErr := os.OpenFile(excludePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if excludeErr == nil {
			defer f.Close()
			_, excludeErr = fmt.Fprintf(f, "\n/%s\n", gerritVersionFilename)
		}
	}
	if excludeErr != nil {
		log.Printf("error adding %q to %q: %v", gerritVersionPath, excludePath, excludeErr)
	}

	return nil
}

func resolveFetchArgs(params inParams, rev *gerrit.RevisionInfo) ([]string, error) {
	fetchUrl := params.FetchUrl
	fetchRef := rev.Ref
	if fetchUrl == "" {
		fetchProtocol := params.FetchProtocol
		if fetchProtocol == "" {
			for _, proto := range defaultFetchProtocols {
				if _, ok := rev.Fetch[proto]; ok {
					fetchProtocol = proto
					break
				}
			}
		}
		fetchInfo, ok := rev.Fetch[fetchProtocol]
		if ok {
			fetchUrl = fetchInfo.URL
			fetchRef = fetchInfo.Ref
		} else {
			return []string{}, fmt.Errorf("no fetch info for protocol %q", fetchProtocol)
		}
	}
	return []string{"fetch", fetchUrl, fetchRef}, nil
}

func git(dir string, args ...string) error {
	gitArgs := append([]string{"-C", dir}, args...)
	log.Printf("git %v", gitArgs)
	output, err := execGit(gitArgs...)
	log.Printf("git output:\n%s", output)
	if err != nil {
		err = fmt.Errorf("git failed: %v", err)
	}
	return err
}

func realExecGit(args ...string) ([]byte, error) {
	return exec.Command("git", args...).CombinedOutput()
}

func buildRevisionLink(src Source, changeNum int, psNum int) (string, error) {
	srcUrl, err := url.Parse(src.Url)
	if err != nil {
		return "", err
	}
	srcUrl.Path = path.Join(srcUrl.Path, fmt.Sprintf("c/%d/%d", changeNum, psNum))
	return srcUrl.String(), nil
}
