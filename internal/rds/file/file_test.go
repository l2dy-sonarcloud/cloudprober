// Copyright 2021-2024 The Cloudprober Authors.
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

package file

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	configpb "github.com/cloudprober/cloudprober/internal/rds/file/proto"
	"github.com/cloudprober/cloudprober/internal/rds/file/testdata"
	rdspb "github.com/cloudprober/cloudprober/internal/rds/proto"
	"google.golang.org/protobuf/proto"
)

var testResourcesFiles = map[string][]string{
	"textpb": {"testdata/targets1.textpb", "testdata/targets2.textpb"},
	"json":   {"testdata/targets.json"},
	"yaml":   {"testdata/targets.yaml"},
}

var testExpectedResources = testdata.ExpectedResources

func compareResourceList(t *testing.T, got []*rdspb.Resource, want []*rdspb.Resource) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("Got resources: %d, expected: %d", len(got), len(want))
	}
	for i := range want {
		if got[i].String() != want[i].String() {
			t.Errorf("ListResources: got[%d]:\n%s\nexpected[%d]:\n%s", i, got[i].String(), i, want[i].String())
		}
	}
}

func TestListResources(t *testing.T) {
	for _, filetype := range []string{"textpb", "json", "yaml"} {
		t.Run(filetype, func(t *testing.T) {
			p, err := New(&configpb.ProviderConfig{FilePath: testResourcesFiles[filetype]}, nil)
			if err != nil {
				t.Fatalf("Unexpected error while creating new provider: %v", err)
			}

			for _, test := range []struct {
				desc          string
				resourcePath  string
				f             []*rdspb.Filter
				wantResources []*rdspb.Resource
			}{
				{
					desc:          "no_filter",
					wantResources: testExpectedResources,
				},
				{
					desc: "with_filter",
					f: []*rdspb.Filter{
						{
							Key:   proto.String("labels.cluster"),
							Value: proto.String("xx"),
						},
					},
					wantResources: testExpectedResources[:2],
				},
			} {
				t.Run(test.desc, func(t *testing.T) {
					got, err := p.ListResources(&rdspb.ListResourcesRequest{Filter: test.f})
					if err != nil {
						t.Fatalf("Unexpected error while listing resources: %v", err)
					}
					compareResourceList(t, got.Resources, test.wantResources)
				})
			}
		})
	}
}

func TestListResourcesWithResourcePath(t *testing.T) {
	p, err := New(&configpb.ProviderConfig{FilePath: testResourcesFiles["textpb"]}, nil)
	if err != nil {
		t.Fatalf("Unexpected error while creating new provider: %v", err)
	}
	got, err := p.ListResources(&rdspb.ListResourcesRequest{ResourcePath: proto.String(testResourcesFiles["textpb"][1])})
	if err != nil {
		t.Fatalf("Unexpected error while listing resources: %v", err)
	}
	compareResourceList(t, got.Resources, testExpectedResources[2:])
}

func BenchmarkListResources(b *testing.B) {
	for _, n := range []int{100, 10000, 1000000} {
		for _, filters := range [][]*rdspb.Filter{nil, []*rdspb.Filter{{Key: proto.String("name"), Value: proto.String("host-1.*")}}} {
			b.Run(fmt.Sprintf("%d-resources,%d-filters", n, len(filters)), func(b *testing.B) {
				b.StopTimer()
				ls := &lister{
					resources: make([]*rdspb.Resource, n),
				}
				for i := 0; i < n; i++ {
					ls.resources[i] = &rdspb.Resource{
						Name: proto.String(fmt.Sprintf("host-%d", i)),
						Ip:   proto.String("10.1.1.1"),
						Port: proto.Int32(80),
						Labels: map[string]string{
							"index": strconv.Itoa(i),
						},
						LastUpdated: proto.Int64(time.Now().Unix()),
					}
				}
				b.StartTimer()

				for j := 0; j < b.N; j++ {
					res, err := ls.listResources(&rdspb.ListResourcesRequest{
						Filter: filters,
					})

					if err != nil {
						b.Errorf("Unexpected error while listing resources: %v", err)
					}

					if filters == nil && len(res.GetResources()) != n {
						b.Errorf("Got %d resources, wanted: %d", len(res.GetResources()), n)
					}
				}
			})
		}
	}
}

func testModTimeCheckBehavior(t *testing.T, disableModTimeCheck bool) {
	t.Helper()
	// Set up test file.
	tf, err := os.CreateTemp("", "cloudprober_rds_file.*.json")
	if err != nil {
		t.Fatal(err)
	}

	testFile := tf.Name()
	defer os.Remove(tf.Name())

	b, err := os.ReadFile(testResourcesFiles["json"][0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testFile, b, 0); err != nil {
		t.Fatal(err)
	}

	ls, err := newLister(testFile, &configpb.ProviderConfig{
		DisableModifiedTimeCheck: proto.Bool(disableModTimeCheck),
	}, nil)
	if err != nil {
		t.Fatalf("Error creating file lister: %v", err)
	}

	// Step 1: Very first run. File should be loaded.
	res, err := ls.listResources(nil)
	if err != nil {
		t.Errorf("Unexxpected error: %v", err)
	}
	if len(res.GetResources()) == 0 {
		t.Error("Got no resources.")
	}
	wantResources := res
	firstUpdateTime := ls.lastUpdated

	// Step 2: 2nd run. File shouldn't reload unless disableModTimeCheck is true.
	// Wait for a second and refresh again.
	time.Sleep(time.Second)
	ls.refresh()

	if !disableModTimeCheck {
		if ls.lastUpdated != firstUpdateTime {
			t.Errorf("File unexpectedly reloaded. Update time: %v, last update time: %v", ls.lastUpdated, firstUpdateTime)
		}
	} else {
		if ls.lastUpdated == firstUpdateTime {
			t.Errorf("File unexpectly didn't reload. Update time: %v, last update time: %v", ls.lastUpdated, firstUpdateTime)
		}
	}
	res, err = ls.listResources(nil)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	wantResources.LastModified = proto.Int64(ls.lastModified())
	if !proto.Equal(res, wantResources) {
		t.Errorf("Got resources:\n%s\nWant resources:\n%s", res.String(), wantResources.String())
	}

	// Step 3: Third run. It should reload file.
	// Update file's modified time and see if file is reloaded.
	fileModTime := time.Now()
	if err := os.Chtimes(testFile, fileModTime, fileModTime); err != nil {
		t.Logf("Error setting modified time on the test file: %v. Finishing test early.", err)
		return
	}
	ls.refresh()

	if ls.lastUpdated.Before(fileModTime) {
		t.Errorf("File lister last update time (%v) before file mod time (%v)", ls.lastUpdated, fileModTime)
	}
	res, err = ls.listResources(nil)
	if err != nil {
		t.Errorf("Unexxpected error: %v", err)
	}
	wantResources.LastModified = proto.Int64(ls.lastModified())
	if !proto.Equal(res, wantResources) {
		t.Errorf("Got resources:\n%s\nWant resources:\n%s", res.String(), wantResources.String())
	}
}

func TestModTimeCheckBehavior(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		testModTimeCheckBehavior(t, false)
	})

	t.Run("ignore-mod-time", func(t *testing.T) {
		testModTimeCheckBehavior(t, true)
	})
}

func TestListResourcesWithCache(t *testing.T) {
	// We test with a provider that contains two listers (created from textpb
	// files above). We try accessing single lister (by setting resource path)
	// and both listers.
	tests := []struct {
		desc               string
		filePaths          [2]string // Lister's file paths.
		listerLastModified [2]int64  // Last modified timestamp for listers.
		ifModifiedSince    int64     // Request's if_modified_since
		resourcePath       string    // Request's resource path
		wantResponse       *rdspb.ListResourcesResponse
	}{
		{
			desc:      "no-caching-all-resources",
			filePaths: [2]string{"testdata/targets1.textpb", "testdata/targets2.textpb"},
			wantResponse: &rdspb.ListResourcesResponse{
				Resources:    testExpectedResources,
				LastModified: proto.Int64(0),
			},
		},
		{
			desc:               "non-zero-last-modified,return-all-resources",
			filePaths:          [2]string{"testdata/targets1.textpb", "testdata/targets2.textpb"},
			listerLastModified: [2]int64{300, 314},
			wantResponse: &rdspb.ListResourcesResponse{
				Resources:    testExpectedResources,
				LastModified: proto.Int64(314),
			},
		},
		{
			desc:               "if-modified-since-older-1,return-all-resources",
			filePaths:          [2]string{"testdata/targets1.textpb", "testdata/targets2.textpb"},
			listerLastModified: [2]int64{300, 314},
			ifModifiedSince:    300,
			wantResponse: &rdspb.ListResourcesResponse{
				Resources:    testExpectedResources,
				LastModified: proto.Int64(314),
			},
		},
		{
			desc:               "if-modified-since-older-2,return-all-resources",
			filePaths:          [2]string{"testdata/targets1.textpb", "testdata/targets2.textpb"},
			listerLastModified: [2]int64{300, 314},
			ifModifiedSince:    302,
			wantResponse: &rdspb.ListResourcesResponse{
				Resources:    testExpectedResources,
				LastModified: proto.Int64(314),
			},
		},
		{
			desc:               "one-resource-path-1st-file,cached",
			filePaths:          [2]string{"testdata/targets1.textpb", "testdata/targets2.textpb"},
			listerLastModified: [2]int64{300, 314},
			ifModifiedSince:    300,
			resourcePath:       "testdata/targets1.textpb",
			wantResponse: &rdspb.ListResourcesResponse{
				LastModified: proto.Int64(300),
			},
		},
		{
			desc:               "one-resource-path-2nd-file,uncached",
			filePaths:          [2]string{"testdata/targets1.textpb", "testdata/targets2.textpb"},
			listerLastModified: [2]int64{300, 314},
			ifModifiedSince:    300,
			resourcePath:       "testdata/targets2.textpb",
			wantResponse: &rdspb.ListResourcesResponse{
				Resources:    testExpectedResources[2:],
				LastModified: proto.Int64(314),
			},
		},
		{
			desc:               "if-modified-since-equal-no-resources",
			ifModifiedSince:    314,
			listerLastModified: [2]int64{300, 314},
			wantResponse: &rdspb.ListResourcesResponse{
				LastModified: proto.Int64(314),
			},
		},
		{
			desc:               "if-modified-since-bigger-no-resources",
			ifModifiedSince:    315,
			listerLastModified: [2]int64{300, 314},
			wantResponse: &rdspb.ListResourcesResponse{
				LastModified: proto.Int64(314),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			p := &Provider{
				filePaths: test.filePaths[:],
				listers:   make(map[string]*lister),
			}

			for i, fp := range test.filePaths {
				ls, _ := newLister(fp, &configpb.ProviderConfig{}, nil)
				ls.lastUpdated = time.Unix(test.listerLastModified[i], 0)
				p.listers[fp] = ls
			}

			resp, err := p.ListResources(&rdspb.ListResourcesRequest{
				ResourcePath:    proto.String(test.resourcePath),
				IfModifiedSince: proto.Int64(test.ifModifiedSince),
			})

			if err != nil {
				t.Errorf("Got unexpected error: %v", err)
				return
			}

			if !proto.Equal(resp, test.wantResponse) {
				t.Errorf("Got response:\n%s\nwanted:\n%s", resp.String(), test.wantResponse.String())
			}
		})
	}
}
