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
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"path/filepath"

	"golang.org/x/build/gerrit"

	"github.com/google/concourse-resources/internal"
)

type outParams struct {
	Repository  string         `json:"repository"`
	Message     string         `json:"message"`
	MessageFile string         `json:"message_file"`
	Labels      map[string]int `json:"labels"`
}

func init() {
	internal.RegisterOutFunc(out)
}

func out(req internal.OutRequest) error {
	var src Source
	var params outParams
	err := req.Decode(&src, &params)
	if err != nil {
		return err
	}

	authMan := newAuthManager(src)
	defer authMan.cleanup()

	// Read gerrit_version.json
	var ver Version
	if params.Repository == "" {
		return errors.New("param repository required")
	}
	gerritVersionPath := filepath.Join(
		req.TargetDir(), params.Repository, gerritVersionFilename)
	err = ver.ReadFromFile(gerritVersionPath)
	if err != nil {
		return fmt.Errorf("error reading %q: %v", gerritVersionPath, err)
	}
	req.SetResponseVersion(ver)

	// Build comment message
	message := params.Message

	if messageFile := params.MessageFile; messageFile != "" {
		var messageBytes []byte
		messageBytes, err = ioutil.ReadFile(filepath.Join(req.TargetDir(), messageFile))
		if err == nil {
			message = string(messageBytes)
		} else {
			log.Printf("error reading message file %q: %v", messageFile, err)
			if message == "" {
				return errors.New("no fallback message; failing")
			} else {
				log.Printf("using fallback message %q", message)
			}
		}
	}

	// Send review
	c, err := gerritClient(src, authMan)
	if err != nil {
		return fmt.Errorf("error setting up gerrit client: %v", err)
	}

	ctx := context.Background()

	err = c.SetReview(ctx, ver.ChangeId, ver.Revision, gerrit.ReviewInput{
		Message: message,
		Labels:  params.Labels,
	})
	if err != nil {
		return fmt.Errorf("error sending review: %v", err)
	}

	return nil
}
