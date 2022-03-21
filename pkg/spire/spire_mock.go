/*
Copyright 2019 The Tekton Authors

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

package spire

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
)

// SpireMockClient is a client used for mocking the this package for unit testing
// other tekton components that use the spire entrypointer or controller client.
//
// The SpireMockClient implements both SpireControllerApiClient and SpireEntrypointerApiClient
// and in addition to that provides the helper functions to define and query internal state.
type SpireMockClient struct {
	// Entries is a dictionary of entries that mock the SPIRE server datastore (for function Sign only)
	Entries map[string]bool

	// SignIdentities represents the list of identities to use to sign (providing context of a caller to Sign)
	// when Sign is called, the identity is dequeued from the slice. A signature will only be provided if the
	// corresponding entry is in Entries. This only takes effect if SignOverride is nil.
	SignIdentities []string

	// VerifyAlwaysReturns defines whether to always verify successfully or to always fail verification if non-nil.
	// This only take effect on Verify functions:
	// - VerifyStatusInternalAnnotationOverride
	// - VerifyTaskRunResultsOverride
	VerifyAlwaysReturns *bool

	// VerifyStatusInternalAnnotationOverride contains the function to overwrite a call to VerifyStatusInternalAnnotation
	VerifyStatusInternalAnnotationOverride func(ctx context.Context, tr *v1beta1.TaskRun, logger *zap.SugaredLogger) error

	// VerifyTaskRunResultsOverride contains the function to overwrite a call to VerifyTaskRunResults
	VerifyTaskRunResultsOverride func(ctx context.Context, prs []v1beta1.PipelineResourceResult, tr *v1beta1.TaskRun) error

	// AppendStatusInternalAnnotationOverride  contains the function to overwrite a call to AppendStatusInternalAnnotation
	AppendStatusInternalAnnotationOverride func(ctx context.Context, tr *v1beta1.TaskRun) error

	// CheckSpireVerifiedFlagOverride contains the function to overwrite a call to CheckSpireVerifiedFlag
	CheckSpireVerifiedFlagOverride func(tr *v1beta1.TaskRun) bool

	// SignOverride contains the function to overwrite a call to Sign
	SignOverride func(ctx context.Context, results []v1beta1.PipelineResourceResult) ([]v1beta1.PipelineResourceResult, error)
}

const (
	controllerSvid = "CONTROLLER_SVID_DATA"
)

func (_ *SpireMockClient) mockSign(content, signedBy string) string {
	return fmt.Sprintf("signed-by-%s:%x", signedBy, sha256.Sum256([]byte(content)))
}

func (sc *SpireMockClient) mockVerify(content, sig, signedBy string) bool {
	return sig == sc.mockSign(content, signedBy)
}

func (_ *SpireMockClient) GetIdentity(tr *v1beta1.TaskRun) string {
	return fmt.Sprintf("/ns/%v/taskrun/%v", tr.Namespace, tr.Name)
}

func (sc *SpireMockClient) AppendStatusInternalAnnotation(ctx context.Context, tr *v1beta1.TaskRun) error {
	// Add status hash
	currentHash, err := hashTaskrunStatusInternal(tr)
	if err != nil {
		return err
	}

	if tr.Status.Annotations == nil {
		tr.Status.Annotations = map[string]string{}
	}
	tr.Status.Annotations[controllerSvidAnnotation] = controllerSvid
	tr.Status.Annotations[TaskRunStatusHashAnnotation] = currentHash
	tr.Status.Annotations[taskRunStatusHashSigAnnotation] = sc.mockSign(currentHash, "controller")
	return nil
}

func (sc *SpireMockClient) CheckSpireVerifiedFlag(tr *v1beta1.TaskRun) bool {
	if sc.CheckSpireVerifiedFlagOverride != nil {
		return sc.CheckSpireVerifiedFlagOverride(tr)
	}

	if _, notVerified := tr.Status.Annotations[NotVerifiedAnnotation]; !notVerified {
		return true
	}
	return false
}

func (sc *SpireMockClient) CreateEntries(ctx context.Context, tr *v1beta1.TaskRun, pod *corev1.Pod, ttl int) error {
	id := fmt.Sprintf("/ns/%v/taskrun/%v", tr.Namespace, tr.Name)
	if sc.Entries == nil {
		sc.Entries = map[string]bool{}
	}
	sc.Entries[id] = true
	return nil
}

func (sc *SpireMockClient) DeleteEntry(ctx context.Context, tr *v1beta1.TaskRun, pod *corev1.Pod) error {
	id := fmt.Sprintf("/ns/%v/taskrun/%v", tr.Namespace, tr.Name)
	if sc.Entries != nil {
		delete(sc.Entries, id)
	}
	return nil
}

func (sc *SpireMockClient) VerifyStatusInternalAnnotation(ctx context.Context, tr *v1beta1.TaskRun, logger *zap.SugaredLogger) error {
	if sc.VerifyStatusInternalAnnotationOverride != nil {
		return sc.VerifyStatusInternalAnnotationOverride(ctx, tr, logger)
	}

	if sc.VerifyAlwaysReturns != nil {
		if *sc.VerifyAlwaysReturns {
			return nil
		} else {
			return errors.New("failed to verify from mock VerifyAlwaysReturns")
		}
	}

	if !sc.CheckSpireVerifiedFlag(tr) {
		return errors.New("annotation tekton.dev/not-verified = yes. Failed spire verification.")
	}

	annotations := tr.Status.Annotations

	// Verify annotations are there
	if annotations[controllerSvidAnnotation] != controllerSvid {
		return errors.New("svid annotation missing")
	}

	// Check signature
	currentHash, err := hashTaskrunStatusInternal(tr)
	if err != nil {
		return err
	}
	if !sc.mockVerify(currentHash, annotations[taskRunStatusHashSigAnnotation], "controller") {
		return errors.New("signature was not able to be verified")
	}

	// check current status hash vs annotation status hash by controller
	if err := CheckStatusInternalAnnotation(tr); err != nil {
		return err
	}

	return nil
}

func (sc *SpireMockClient) VerifyTaskRunResults(ctx context.Context, prs []v1beta1.PipelineResourceResult, tr *v1beta1.TaskRun) error {
	if sc.VerifyTaskRunResultsOverride != nil {
		return sc.VerifyTaskRunResultsOverride(ctx, prs, tr)
	}

	if sc.VerifyAlwaysReturns != nil {
		if *sc.VerifyAlwaysReturns {
			return nil
		} else {
			return errors.New("failed to verify from mock VerifyAlwaysReturns")
		}
	}

	resultMap := map[string]v1beta1.PipelineResourceResult{}
	for _, r := range prs {
		if r.ResultType == v1beta1.TaskRunResultType {
			resultMap[r.Key] = r
		}
	}

	var identity string
	// Get SVID identity
	for k, p := range resultMap {
		if k == KeySVID {
			identity = p.Value
			break
		}
	}

	// Verify manifest
	if err := verifyManifest(resultMap); err != nil {
		return err
	}

	if identity != sc.GetIdentity(tr) {
		return errors.New("mock identity did not match")
	}

	for key, r := range resultMap {
		if strings.HasSuffix(key, KeySignatureSuffix) {
			continue
		}
		if key == KeySVID {
			continue
		}

		sigEntry, ok := resultMap[key+KeySignatureSuffix]
		if !ok || !sc.mockVerify(r.Value, sigEntry.Value, identity) {
			return errors.Errorf("failed to verify field: %v", key)
		}
	}

	return nil
}

func (sc *SpireMockClient) Sign(ctx context.Context, results []v1beta1.PipelineResourceResult) ([]v1beta1.PipelineResourceResult, error) {
	if sc.SignOverride != nil {
		return sc.SignOverride(ctx, results)
	}

	if len(sc.SignIdentities) == 0 {
		return nil, errors.New("signIdentities empty, please provide identities to sign with the SpireMockClient.GetIdentity function")
	}

	identity := sc.SignIdentities[0]
	sc.SignIdentities = sc.SignIdentities[1:]

	if !sc.Entries[identity] {
		return nil, errors.Errorf("entry doesn't exist for identity: %v", identity)
	}

	output := []v1beta1.PipelineResourceResult{}
	output = append(output, v1beta1.PipelineResourceResult{
		Key:        KeySVID,
		Value:      identity,
		ResultType: v1beta1.TaskRunResultType,
	})

	for _, r := range results {
		if r.ResultType == v1beta1.TaskRunResultType {
			s := sc.mockSign(r.Value, identity)
			output = append(output, v1beta1.PipelineResourceResult{
				Key:        r.Key + KeySignatureSuffix,
				Value:      s,
				ResultType: v1beta1.TaskRunResultType,
			})
		}
	}
	// get complete manifest of keys such that it can be verified
	manifest := getManifest(results)
	output = append(output, v1beta1.PipelineResourceResult{
		Key:        KeyResultManifest,
		Value:      manifest,
		ResultType: v1beta1.TaskRunResultType,
	})
	manifestSig := sc.mockSign(manifest, identity)
	output = append(output, v1beta1.PipelineResourceResult{
		Key:        KeyResultManifest + KeySignatureSuffix,
		Value:      manifestSig,
		ResultType: v1beta1.TaskRunResultType,
	})

	return output, nil
}

func (sc *SpireMockClient) Close() {}