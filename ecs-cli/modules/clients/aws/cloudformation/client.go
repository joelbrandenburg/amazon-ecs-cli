// Copyright 2015-2017 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//	http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package cloudformation

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/amazon-ecs-cli/ecs-cli/modules/clients"
	"github.com/aws/amazon-ecs-cli/ecs-cli/modules/config"
	"github.com/aws/amazon-ecs-cli/ecs-cli/modules/utils"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface"
	log "github.com/sirupsen/logrus"
)

const (
	// maxRetriesCreate is the maximum number of DescribeStackEvents API will be invoked by the WaitUntilCreateComplete method
	// to determine if the stack was created successfully before giving up. This value reflects the values set in the
	// cloudformation waiters json file in the aws-go-sdk.
	maxRetriesCreate = 50

	// maxRetriesDelete is the maximum number of DescribeStackEvents API will be invoked by the WaitUntilDeleteComplete method
	// to determine if the stack was deleted successfully before giving up. This value reflects the values set in the
	// cloudformation waiters json file in the aws-go-sdk.
	maxRetriesDelete = 25

	// maxRetriesUpdate is the maximum number of DescribeStackEvents API will be invoked by the WaitUntilUpdateComplete method
	// to determine if the stack was updated successfully before giving up. This value reflects the values set in the
	// cloudformation waiters json file in the aws-go-sdk.
	maxRetriesUpdate = 5

	// delayWait is the delay between successive DescribeStackEvents API calls while determining if the stack was created. This value
	// reflects the values set in the cloudformation waiters json file in the aws-go-sdk.
	delayWait = 30 * time.Second

	validationErrorCode = "ValidationError"
)

// createStackFailures maps all known cloudformation stack creation failure statuses to boolean values. It is
// used for faster lookup of stack status to determine creation failures.
var createStackFailures map[string]bool

// deleteStackFailures maps all known cloudformation stack creation failure statuses to boolean values. It is
// used for faster lookup of stack status to determine creation failures.
var deleteStackFailures map[string]bool

// updateStackFailures maps all known cloudformation stack update failure statuses to boolean values. It is
// used for faster lookup of stack status to determine update failures.
var updateStackFailures map[string]bool

func init() {
	// Populate all the failure status messages that we'd likely see while creating, deleting and updating
	// the cloudformation stack.
	//
	// Reference:
	// http://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/using-cfn-describing-stacks.html
	createStackFailures = map[string]bool{
		cloudformation.StackStatusCreateFailed:         true,
		cloudformation.StackStatusRollbackInProgress:   true,
		cloudformation.StackStatusRollbackComplete:     true,
		cloudformation.StackStatusUpdateRollbackFailed: true,
	}

	deleteStackFailures = map[string]bool{
		cloudformation.StackStatusDeleteFailed: true,
	}

	updateStackFailures = map[string]bool{
		cloudformation.StackStatusUpdateRollbackComplete: true,
		cloudformation.StackStatusUpdateRollbackFailed:   true,
	}
}

// CloudformationClient defines methods to interact the with the CloudFormationAPI interface.
type CloudformationClient interface {
	CreateStack(string, string, *CfnStackParams) (string, error)
	WaitUntilCreateComplete(string) error
	DeleteStack(string) error
	WaitUntilDeleteComplete(string) error
	UpdateStack(string, *CfnStackParams) (string, error)
	WaitUntilUpdateComplete(string) error
	ValidateStackExists(string) error
	DescribeNetworkResources(string) error
	GetStackParameters(string) ([]*cloudformation.Parameter, error)
}

// cloudformationClient implements CloudFormationClient.
type cloudformationClient struct {
	client  cloudformationiface.CloudFormationAPI
	config  *config.CommandConfig
	sleeper utils.Sleeper
}

// NewCloudformationClient creates an instance of cloudFormationClient object.
func NewCloudformationClient(config *config.CommandConfig) CloudformationClient {
	cfnClient := cloudformation.New(config.Session)
	cfnClient.Handlers.Build.PushBackNamed(clients.CustomUserAgentHandler())

	return newClient(config, cfnClient)
}

func newClient(config *config.CommandConfig, client cloudformationiface.CloudFormationAPI) CloudformationClient {
	return &cloudformationClient{
		config:  config,
		client:  client,
		sleeper: &utils.TimeSleeper{},
	}
}

// CreateStack creates the cloudformation stack by invoking the sdk's CreateStack API and returns the stack id.
func (c *cloudformationClient) CreateStack(template string, stackName string, params *CfnStackParams) (string, error) {
	output, err := c.client.CreateStack(&cloudformation.CreateStackInput{
		TemplateBody: aws.String(template),
		Capabilities: aws.StringSlice([]string{cloudformation.CapabilityCapabilityIam}),
		StackName:    aws.String(stackName),
		Parameters:   params.Get(),
	})

	if err != nil {
		return "", err
	}

	log.WithFields(log.Fields{"stackId": output.StackId}).Debug("Cloudformation create stack call succeeded")
	return aws.StringValue(output.StackId), nil
}

// DeleteStack deletes the cloudformation stack.
func (c *cloudformationClient) DeleteStack(stackName string) error {
	_, err := c.client.DeleteStack(&cloudformation.DeleteStackInput{
		StackName: aws.String(stackName),
	})

	return err
}

// UpdateStack creates the cloudformation stack by invoking the sdk's UpdateStack API.
func (c *cloudformationClient) UpdateStack(stackName string, params *CfnStackParams) (string, error) {
	output, err := c.client.UpdateStack(&cloudformation.UpdateStackInput{
		Capabilities:        aws.StringSlice([]string{cloudformation.CapabilityCapabilityIam}),
		StackName:           aws.String(stackName),
		Parameters:          params.Get(),
		UsePreviousTemplate: aws.Bool(true),
	})

	if err != nil {
		return "", err
	}

	log.WithFields(log.Fields{"stackId": output.StackId}).Debug("Cloudformation update stack call succeeded")
	return aws.StringValue(output.StackId), nil
}

// ValidateStackExists validates if a stack exists with the specified name.
func (c *cloudformationClient) ValidateStackExists(stackName string) error {
	_, err := c.describeStack(stackName)
	return err
}

// describeStack describes the stack and gets the stack status.
func (c *cloudformationClient) GetStackParameters(stackName string) ([]*cloudformation.Parameter, error) {
	output, err := c.client.DescribeStacks(&cloudformation.DescribeStacksInput{
		StackName: aws.String(stackName),
	})

	if err != nil {
		return nil, err
	}

	if len(output.Stacks) == 0 {
		return nil, fmt.Errorf("Could not describe stack '%s'", stackName)
	}

	return output.Stacks[0].Parameters, nil
}

// WaitUntilCreateComplete waits until the stack creation completes.
func (c *cloudformationClient) WaitUntilCreateComplete(stackName string) error {
	return c.waitUntilComplete(stackName, failureInCreateEvent, cloudformation.StackStatusCreateComplete, createStackFailures, maxRetriesCreate)
}

// WaitUntilDeleteComplete waits until the stack deletion completes.
func (c *cloudformationClient) WaitUntilDeleteComplete(stackName string) error {
	err := c.waitUntilComplete(stackName, failureInDeleteEvent, cloudformation.StackStatusDeleteComplete, deleteStackFailures, maxRetriesDelete)
	if err != nil {
		awsError, ok := err.(awserr.Error)
		// if we got a validation error which said stack does not exist, then the stack was deleted successfully
		// then continue, else return the error
		// TODO: ListStacks and check StackSummaries[n].StackStatus == "DELETE_COMPLETE"
		if ok && awsError.Code() == validationErrorCode && strings.Contains(awsError.Message(), "does not exist") {
			return nil
		}
		return err
	}
	return nil
}

// WaitUntilUpdateComplete waits until the stack update completes.
func (c *cloudformationClient) WaitUntilUpdateComplete(stackName string) error {
	return c.waitUntilComplete(stackName, failureInUpdateEvent, cloudformation.StackStatusUpdateComplete, updateStackFailures, maxRetriesUpdate)
}

// failureInStackEvent defines the callback type, which determines if there's the cloudformation
// stack event's status indicates failure in creating/updating/deleting a resource.
type failureInStackEvent func(*cloudformation.StackEvent) bool

// waitUntilComplete waits until the function callback indicates completeness or until maxRetries are exhausted.
func (c *cloudformationClient) waitUntilComplete(stackName string, hasFailed failureInStackEvent, successState string, failureStates map[string]bool, maxRetries int) error {
	for retryCount := 0; retryCount < maxRetries; retryCount++ {
		event, err := c.latestStackEvent(stackName)
		if err != nil {
			return err
		}
		if failed := hasFailed(event); failed {
			reason := aws.StringValue(event.ResourceStatusReason)
			return fmt.Errorf("Cloudformation failure waiting for '%s'. Reason: '%s'", successState, reason)
		}

		// No errors in stack events. Query stack status.
		status, err := c.describeStack(stackName)
		if err != nil {
			return err
		}

		if successState == status {
			return nil
		}

		_, exists := failureStates[status]
		if exists {
			log.Debug("Stack creation failed. Getting first failed event")
			if failureEvent, err := c.firstStackEventWithFailure(stackName, nil, failureStates); err == nil {
				log.WithFields(log.Fields{
					"reason":       aws.StringValue(failureEvent.ResourceStatusReason),
					"resourceType": aws.StringValue(failureEvent.ResourceType),
				}).Error("Failure event")
			}
			return fmt.Errorf("Cloudformation failure waiting for '%s'. State is '%s'", successState, status)
		}

		if retryCount%2 == 0 {
			log.WithFields(log.Fields{"stackStatus": status}).Info("Cloudformation stack status")
		} else {
			log.WithFields(log.Fields{"stackStatus": status}).Debug("Cloudformation stack status")
		}
		c.sleeper.Sleep(delayWait)
	}

	return fmt.Errorf("Timeout waiting for stack operation to complete")
}

// latestStackEvent describes stack events and gets the latest event.
func (c *cloudformationClient) latestStackEvent(stackName string) (*cloudformation.StackEvent, error) {
	response, err := c.client.DescribeStackEvents(&cloudformation.DescribeStackEventsInput{StackName: aws.String(stackName)})
	if err != nil {
		return nil, err
	}

	if len(response.StackEvents) == 0 {
		return nil, fmt.Errorf("Could not describe stack events")
	}

	return response.StackEvents[0], nil
}

// firstStackEventWithFailure describes stack events and gets the latest event.
func (c *cloudformationClient) firstStackEventWithFailure(stackName string, nextToken *string, failureStates map[string]bool) (*cloudformation.StackEvent, error) {
	response, err := c.client.DescribeStackEvents(&cloudformation.DescribeStackEventsInput{
		StackName: aws.String(stackName),
		NextToken: nextToken,
	})
	if err != nil {
		return nil, err
	}

	if len(response.StackEvents) == 0 {
		return nil, fmt.Errorf("Could not describe stack events")
	}

	if response.NextToken != nil {
		return c.firstStackEventWithFailure(stackName, response.NextToken, failureStates)
	}

	for i := len(response.StackEvents) - 1; i >= 0; i-- {
		event := response.StackEvents[i]
		log.WithFields(log.Fields{
			"status":       aws.StringValue(event.ResourceStatus),
			"reason":       aws.StringValue(event.ResourceStatusReason),
			"id":           aws.StringValue(event.EventId),
			"resourceType": aws.StringValue(event.ResourceType),
		}).Debug("Parsing event")
		if _, exists := failureStates[aws.StringValue(event.ResourceStatus)]; exists {
			return event, nil
		}
	}

	return nil, fmt.Errorf("Unable to find failure event in stack '%s'", stackName)
}

// describeStack describes the stack and gets the stack status.
func (c *cloudformationClient) describeStack(stackName string) (string, error) {
	output, err := c.client.DescribeStacks(&cloudformation.DescribeStacksInput{
		StackName: aws.String(stackName),
	})

	if err != nil {
		return "", err
	}

	if 0 == len(output.Stacks) {
		return "", fmt.Errorf("Could not describe stack '%s'", stackName)
	}

	return aws.StringValue(output.Stacks[0].StackStatus), nil
}

func (c *cloudformationClient) describeStackResource(stackName string, logicalResourceId string) (*cloudformation.StackResource, error) {
	input := &cloudformation.DescribeStackResourcesInput{
		StackName:         aws.String(stackName),
		LogicalResourceId: aws.String(logicalResourceId),
	}

	output, err := c.client.DescribeStackResources(input)

	if err != nil {
		return nil, err
	}

	if len(output.StackResources) > 0 {
		resource := output.StackResources[0]
		return resource, nil
	}

	return nil, nil
}

func displayResourceId(resource *cloudformation.StackResource, name string) {
	if resource != nil {
		id := aws.StringValue(resource.PhysicalResourceId)
		fmt.Printf("%v created: %v\n", name, id)
	}
}

func (c *cloudformationClient) DescribeNetworkResources(stackName string) error {
	// Describe EC2::VPC
	resource, err := c.describeStackResource(stackName, VPCLogicalResourceId)
	if err != nil {
		return err
	}
	displayResourceId(resource, "VPC")

	// Describe EC2::SecurityGroup
	resource, err = c.describeStackResource(stackName, SecurityGroupLogicalResourceId)
	if err != nil {
		return err
	}
	displayResourceId(resource, "Security Group")

	// Describe EC2::Subnets
	subnets := []string{Subnet1LogicalResourceId, Subnet2LogicalResourceId}
	for _, id := range subnets {
		resource, err = c.describeStackResource(stackName, id)
		if err != nil {
			return err
		}
		displayResourceId(resource, "Subnet")
	}

	return nil
}

// failureInCreateEvent returns an error if the stack event indicates that stack creation event has failed.
func failureInCreateEvent(event *cloudformation.StackEvent) bool {
	status := aws.StringValue(event.ResourceStatus)
	log.WithFields(log.Fields{
		"eventStatus": status,
		"resource":    aws.StringValue(event.PhysicalResourceId),
	}).Debug("parsing event")
	if cloudformation.ResourceStatusCreateFailed == status {
		log.WithFields(log.Fields{
			"eventStatus": status,
			"resource":    aws.StringValue(event.PhysicalResourceId),
			"reason":      aws.StringValue(event.ResourceStatusReason),
		}).Error("Error creating cloudformation stack for cluster")
		return true
	}

	return false
}

// failureInDeleteEvent returns true if the stack event indicates that stack deletion is complete.
func failureInDeleteEvent(event *cloudformation.StackEvent) bool {
	status := aws.StringValue(event.ResourceStatus)
	if cloudformation.ResourceStatusDeleteFailed == status {
		log.WithFields(log.Fields{
			"eventStatus": status,
			"resource":    aws.StringValue(event.PhysicalResourceId),
			"reason":      aws.StringValue(event.ResourceStatusReason),
		}).Error("Error deleting cloudformation stack for cluster")
		return true
	}

	return false
}

// failureInUpdateEvent returns true if the stack event indicates that stack update is complete.
func failureInUpdateEvent(event *cloudformation.StackEvent) bool {
	status := aws.StringValue(event.ResourceStatus)
	if cloudformation.ResourceStatusUpdateFailed == status {
		log.WithFields(log.Fields{
			"eventStatus": status,
			"resource":    aws.StringValue(event.PhysicalResourceId),
			"reason":      aws.StringValue(event.ResourceStatusReason),
		}).Error("Error updating cloudformation stack for cluster")
		return true
	}

	return false
}
