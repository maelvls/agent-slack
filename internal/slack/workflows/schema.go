package workflows

import (
	"context"
	"fmt"
	"strings"

	"github.com/maelvls/slackcli/internal/jsonx"
	"github.com/maelvls/slackcli/internal/slackapi"
)

// FormField is one input of a workflow's form.
type FormField struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	// Long marks a multi-line text input.
	Long bool `json:"long,omitempty"`
}

// Schema is a workflow definition, in the shape the CLI prints.
type Schema struct {
	WorkflowID  string `json:"workflow_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// FormTitle and Fields describe the workflow's first form step: the
	// one a user is shown when starting it.
	FormTitle string      `json:"form_title,omitempty"`
	Fields    []FormField `json:"fields"`
	// Steps are the step titles, in order.
	Steps []string `json:"steps"`
}

// GetSchema returns a workflow's definition: its form fields and steps
// (workflows.get). id is a workflow ID (Wf…), or a trigger ID (Ft…) or
// shortcut URL, which is first resolved to its workflow with
// PreviewTrigger.
func GetSchema(ctx context.Context, client *slackapi.Client, id string) (Schema, error) {
	workflowID := strings.TrimSpace(id)
	if workflowID == "" {
		return Schema{}, fmt.Errorf("workflow ID is empty: pass a workflow ID (Wf…) or a trigger ID (Ft…)")
	}
	if trigger, err := ParseTrigger(workflowID); err == nil {
		preview, err := PreviewTrigger(ctx, client, trigger.ID)
		if err != nil {
			return Schema{}, err
		}
		if preview.Workflow.ID == "" {
			return Schema{}, fmt.Errorf("Slack does not report which workflow trigger %s runs; pass the workflow ID (Wf…) instead", trigger.ID)
		}
		workflowID = preview.Workflow.ID
	}

	resp, err := client.API(ctx, "workflows.get", map[string]any{"workflow_id": workflowID})
	if err != nil {
		return Schema{}, fmt.Errorf("getting workflow %s: %w", workflowID, err)
	}
	wf := jsonx.Map(resp["workflow"])
	if wf == nil {
		return Schema{}, fmt.Errorf("no workflow found for ID %s", workflowID)
	}

	schema := Schema{
		WorkflowID:  jsonx.Str(wf["id"]),
		Title:       jsonx.Str(wf["title"]),
		Description: jsonx.Str(wf["description"]),
	}
	if schema.WorkflowID == "" {
		schema.WorkflowID = workflowID
	}
	formSeen := false
	for _, step := range jsonx.Maps(wf["steps"]) {
		callbackID := jsonx.GetStr(step, "function", "callback_id")
		title := jsonx.GetStr(step, "function", "title")
		if title == "" {
			title = callbackID
		}
		schema.Steps = append(schema.Steps, title)

		if callbackID == "open_form" && !formSeen {
			formSeen = true
			schema.FormTitle = jsonx.GetStr(step, "inputs", "title", "value")
			schema.Fields = formFields(jsonx.GetMap(step, "inputs", "fields", "value"))
		}
	}
	return schema, nil
}

func formFields(form map[string]any) []FormField {
	required := map[string]bool{}
	for _, r := range jsonx.Slice(form["required"]) {
		if name := jsonx.Str(r); name != "" {
			required[name] = true
		}
	}
	var fields []FormField
	for _, el := range jsonx.Maps(form["elements"]) {
		name := jsonx.Str(el["name"])
		typ := jsonx.Str(el["type"])
		if typ == "" {
			typ = "string"
		}
		fields = append(fields, FormField{
			Name:        name,
			Title:       jsonx.Str(el["title"]),
			Type:        typ,
			Description: jsonx.Str(el["description"]),
			Required:    required[name],
			Long:        jsonx.Bool(el["long"]),
		})
	}
	return fields
}
