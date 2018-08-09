// Copyright 2015-2018 Amazon.com, Inc. or its affiliates. All Rights Reserved.
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

// Package route53 contains functions for working with the route53 APIs
// that back ECS Service Discovery
package route53

import (
	"github.com/aws/amazon-ecs-cli/ecs-cli/modules/clients"
	"github.com/aws/amazon-ecs-cli/ecs-cli/modules/config"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/aws/aws-sdk-go/service/servicediscovery"
)

// FindPrivateNamespace returns the ID(s) of the private namespace with the given name and vpc
func FindPrivateNamespace(name, vpc string, config *config.CommandConfig) (*string, error) {
	r53Client := newRoute53Client(config)
	sdClient := newSDClient(config)
	return findPrivateNamespace(name, vpc, aws.StringValue(config.Session.Config.Region), r53Client, sdClient)
}

// private function findPrivateNamespace can accept mock client objects, allowing it to be unit tested
func findPrivateNamespace(name, vpc, region string, r53Client route53Client, sdClient serviceDiscoveryClient) (*string, error) {
	var nameMatch []*string

	err := listNamespaces(false, sdClient, func(namespace *servicediscovery.NamespaceSummary) bool {
		if aws.StringValue(namespace.Name) == name {
			nameMatch = append(nameMatch, namespace.Id)
		}
		return true
	})
	if err != nil {
		return nil, err
	}

	for _, namespaceID := range nameMatch {
		hasVPC, err := checkNamespaceVPC(namespaceID, vpc, region, r53Client, sdClient)
		if err != nil {
			return nil, err
		}
		if hasVPC {
			return namespaceID, nil
		}
	}

	return nil, nil
}

// FindPublicNamespace returns the ID of the public namespace with the given name
func FindPublicNamespace(name string, config *config.CommandConfig) (*string, error) {
	sdClient := newSDClient(config)
	return findPublicNamespace(name, sdClient)
}

// private function findPublicNamespace can accept mock client objects, allowing it to be unit tested
func findPublicNamespace(name string, sdClient serviceDiscoveryClient) (*string, error) {
	var namespace *string
	err := listNamespaces(true, sdClient, func(n *servicediscovery.NamespaceSummary) bool {
		if aws.StringValue(n.Name) == name {
			namespace = n.Id
			return false // we found it, stop the list call
		}
		return true
	})

	return namespace, err
}

func checkNamespaceVPC(namespaceID *string, vpc string, region string, r53Client route53Client, sdClient serviceDiscoveryClient) (bool, error) {
	namespaceInfo, err := sdClient.GetNamespace(&servicediscovery.GetNamespaceInput{
		Id: namespaceID,
	})
	if err != nil {
		return false, err
	}
	hostedZoneID := namespaceInfo.Namespace.Properties.DnsProperties.HostedZoneId
	hostedZone, err := r53Client.GetHostedZone(&route53.GetHostedZoneInput{
		Id: hostedZoneID,
	})
	if err != nil {
		return false, err
	}
	for _, hostedZoneVPC := range hostedZone.VPCs {
		// The VPC must be in the region that we will be launching the ECS Service
		if (aws.StringValue(hostedZoneVPC.VPCId) == vpc) && (aws.StringValue(hostedZoneVPC.VPCRegion) == region) {
			return true, nil
		}
	}

	return false, nil
}

// Private ServiceDiscovery Client that can be mocked in unit tests
// The SDK's servicediscovery client implements this interface
type serviceDiscoveryClient interface {
	ListNamespacesPages(input *servicediscovery.ListNamespacesInput, fn func(*servicediscovery.ListNamespacesOutput, bool) bool) error
	GetNamespace(input *servicediscovery.GetNamespaceInput) (*servicediscovery.GetNamespaceOutput, error)
}

// factory function to create clients
func newSDClient(config *config.CommandConfig) serviceDiscoveryClient {
	sdClient := servicediscovery.New(config.Session)
	sdClient.Handlers.Build.PushBackNamed(clients.CustomUserAgentHandler())
	return sdClient
}

// Lists namespaces, calling 'fn' on each namespace returned.
// To stop iterating over namespaces, return false from 'fn'
func listNamespaces(isPublic bool, client serviceDiscoveryClient, fn func(*servicediscovery.NamespaceSummary) bool) error {
	typeFilter := servicediscovery.NamespaceTypeDnsPrivate
	if isPublic {
		typeFilter = servicediscovery.NamespaceTypeDnsPublic
	}
	request := &servicediscovery.ListNamespacesInput{
		Filters: []*servicediscovery.NamespaceFilter{
			&servicediscovery.NamespaceFilter{
				Condition: aws.String(servicediscovery.FilterConditionEq),
				Name:      aws.String(servicediscovery.NamespaceFilterNameType),
				Values:    aws.StringSlice([]string{typeFilter}),
			},
		},
	}
	err := client.ListNamespacesPages(request,
		func(page *servicediscovery.ListNamespacesOutput, lastPage bool) bool {
			for _, namespace := range page.Namespaces {
				if !fn(namespace) {
					return false
				}
			}
			return !lastPage
		})
	return err
}