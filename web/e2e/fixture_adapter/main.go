package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
)

func main() {
	if err := serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "workflow fixture adapter stopped")
		os.Exit(1)
	}
}

func serve(input io.Reader, output io.Writer) error {
	reader := bufio.NewReaderSize(input, 32<<10)
	workflows := map[string]string{}
	for {
		request, err := adapterproto.ReadRequest(reader)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var response adapterproto.Response
		switch request.Method {
		case adapterproto.MethodDescribe:
			response, _ = adapterproto.Success(request.ID, describe())
		case adapterproto.MethodResolveBegin:
			var params adapterproto.ResolveBeginParams
			if err = json.Unmarshal(request.Params, &params); err != nil {
				response = adapterproto.Failure(request.ID, "invalid_params", "resolve input is invalid", nil)
				break
			}
			var input struct {
				SourceURL string `json:"source_url"`
			}
			if err = json.Unmarshal(params.Input, &input); err != nil || strings.TrimSpace(input.SourceURL) == "" {
				response = adapterproto.Failure(request.ID, "invalid_input", "resolve input is invalid", nil)
				break
			}
			workflows[params.WorkflowID] = input.SourceURL
			data, _ := json.Marshal(map[string]any{"url": "https://example.test/confirm", "label": "Open fixture action"})
			challenge := &adapterproto.WorkflowChallenge{
				Schema: adapterproto.Schema{Fields: []adapterproto.Field{
					{Key: "answer", Control: "text", Label: "Fixture answer", Required: true},
					{Key: "session_value", Control: "secret", Label: "Fixture secret", Required: true, Persistence: &adapterproto.FieldPersistence{Mode: adapterproto.PersistenceOptional, Target: adapterproto.PersistenceTarget{Scope: adapterproto.PersistencePlugin}}},
				}},
				Prompt: &adapterproto.InteractionMessage{Type: "action", InteractionID: "browser-e2e-action", Title: "Fixture action", Message: "Open this safe external fixture step if needed.", Data: data},
			}
			response, _ = adapterproto.Success(request.ID, adapterproto.ResolveWorkflowResult{State: "configuration_required", WorkflowID: params.WorkflowID, Challenge: challenge})
		case adapterproto.MethodResolveContinue:
			var params adapterproto.ResolveContinueParams
			if err = json.Unmarshal(request.Params, &params); err != nil {
				response = adapterproto.Failure(request.ID, "invalid_params", "workflow input is invalid", nil)
				break
			}
			manifestURL := workflows[params.WorkflowID]
			if manifestURL == "" || strings.TrimSpace(params.AnswerSecrets["session_value"]) == "" || len(params.Answers["answer"]) == 0 {
				response = adapterproto.Failure(request.ID, "invalid_answer", "workflow input is invalid", nil)
				break
			}
			delete(workflows, params.WorkflowID)
			media := &adapterproto.MediaSource{Type: "hls", ManifestURL: manifestURL}
			response, _ = adapterproto.Success(request.ID, adapterproto.ResolveWorkflowResult{State: "resolved", WorkflowID: params.WorkflowID, Media: media})
		case adapterproto.MethodShutdown:
			response, _ = adapterproto.Success(request.ID, map[string]bool{"stopped": true})
			if err = adapterproto.WriteResponse(output, response); err != nil {
				return err
			}
			return nil
		default:
			response = adapterproto.Failure(request.ID, "unsupported_method", "method is not supported", nil)
		}
		if err = adapterproto.WriteResponse(output, response); err != nil {
			return err
		}
	}
}

func describe() adapterproto.Descriptor {
	return adapterproto.Descriptor{
		ID: "workflow-fixture", Name: "Workflow Fixture", Version: "0.1.0", ProtocolVersion: adapterproto.Version,
		Capabilities:        []string{adapterproto.CapabilityResolveWorkflow},
		InputSchema:         adapterproto.Schema{Fields: []adapterproto.Field{{Key: "source_url", Control: "text", Label: "Fixture source URL", Required: true}}},
		ConfigurationSchema: adapterproto.Schema{Fields: []adapterproto.Field{}},
		ResourceTypes:       []adapterproto.ResourceType{}, MediaTypes: []string{"hls"},
	}
}
