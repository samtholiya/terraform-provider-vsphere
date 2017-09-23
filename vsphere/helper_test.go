package vsphere

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform/helper/resource"
	"github.com/hashicorp/terraform/terraform"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"github.com/vmware/vic/pkg/vsphere/tags"
)

// testCheckVariables bundles common variables needed by various test checkers.
type testCheckVariables struct {
	// A client for various operations.
	client *govmomi.Client

	// The client for tagging operations.
	tagsClient *tags.RestClient

	// The subject resource's ID.
	resourceID string

	// The subject resource's attributes.
	resourceAttributes map[string]string

	// The ESXi host that a various API call is directed at.
	esxiHost string

	// The datacenter that a various API call is directed at.
	datacenter string

	// A timeout to pass to various context creation calls.
	timeout time.Duration
}

func testClientVariablesForResource(s *terraform.State, addr string) (testCheckVariables, error) {
	rs, ok := s.RootModule().Resources[addr]
	if !ok {
		return testCheckVariables{}, fmt.Errorf("%s not found in state", addr)
	}

	return testCheckVariables{
		client:             testAccProvider.Meta().(*VSphereClient).vimClient,
		tagsClient:         testAccProvider.Meta().(*VSphereClient).tagsClient,
		resourceID:         rs.Primary.ID,
		resourceAttributes: rs.Primary.Attributes,
		esxiHost:           os.Getenv("VSPHERE_ESXI_HOST"),
		datacenter:         os.Getenv("VSPHERE_DATACENTER"),
		timeout:            time.Minute * 5,
	}, nil
}

// testAccESXiFlagSet returns true if VSPHERE_TEST_ESXI is set.
func testAccESXiFlagSet() bool {
	return os.Getenv("VSPHERE_TEST_ESXI") != ""
}

// testAccSkipIfNotEsxi skips a test if VSPHERE_TEST_ESXI is not set.
func testAccSkipIfNotEsxi(t *testing.T) {
	if !testAccESXiFlagSet() {
		t.Skip("set VSPHERE_TEST_ESXI to run ESXi-specific acceptance tests")
	}
}

// testAccSkipIfEsxi skips a test if VSPHERE_TEST_ESXI is set.
func testAccSkipIfEsxi(t *testing.T) {
	if testAccESXiFlagSet() {
		t.Skip("test skipped as VSPHERE_TEST_ESXI is set")
	}
}

// expectErrorIfNotVirtualCenter returns the error message that
// validateVirtualCenter returns if VSPHERE_TEST_ESXI is set, to allow for test
// cases that will still run on ESXi, but will expect validation failure.
func expectErrorIfNotVirtualCenter() *regexp.Regexp {
	if testAccESXiFlagSet() {
		return regexp.MustCompile(errVirtualCenterOnly)
	}
	return nil
}

// copyStatePtr returns a TestCheckFunc that copies the reference to the test
// run's state to t. This allows access to the state data in later steps where
// it's not normally accessible (ie: in pre-config parts in another test step).
func copyStatePtr(t **terraform.State) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		*t = s
		return nil
	}
}

// testGetPortGroup is a convenience method to fetch a static port group
// resource for testing.
func testGetPortGroup(s *terraform.State, resourceName string) (*types.HostPortGroup, error) {
	tVars, err := testClientVariablesForResource(s, fmt.Sprintf("vsphere_host_port_group.%s", resourceName))
	if err != nil {
		return nil, err
	}

	hsID, name, err := splitHostPortGroupID(tVars.resourceID)
	if err != nil {
		return nil, err
	}
	ns, err := hostNetworkSystemFromHostSystemID(tVars.client, hsID)
	if err != nil {
		return nil, fmt.Errorf("error loading host network system: %s", err)
	}

	return hostPortGroupFromName(tVars.client, ns, name)
}

// testGetVirtualMachine is a convenience method to fetch a virtual machine by
// resource name.
func testGetVirtualMachine(s *terraform.State, resourceName string) (*object.VirtualMachine, error) {
	tVars, err := testClientVariablesForResource(s, fmt.Sprintf("vsphere_virtual_machine.%s", resourceName))
	if err != nil {
		return nil, err
	}
	uuid, ok := tVars.resourceAttributes["uuid"]
	if !ok {
		return nil, fmt.Errorf("resource %q has no UUID", resourceName)
	}
	return virtualMachineFromUUID(tVars.client, uuid)
}

// testGetVirtualMachineProperties is a convenience method that adds an extra
// step to testGetVirtualMachine to get the properties of a virtual machine.
func testGetVirtualMachineProperties(s *terraform.State, resourceName string) (*mo.VirtualMachine, error) {
	vm, err := testGetVirtualMachine(s, resourceName)
	if err != nil {
		return nil, err
	}
	return virtualMachineProperties(vm)
}

// testPowerOffVM does an immediate power-off of the supplied virtual machine
// resource defined by the supplied resource address name. It is used to help
// set up a test scenarios where a VM is powered off.
func testPowerOffVM(s *terraform.State, resourceName string) error {
	vm, err := testGetVirtualMachine(s, resourceName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer cancel()
	task, err := vm.PowerOff(ctx)
	if err != nil {
		return fmt.Errorf("error powering off VM: %s", err)
	}
	tctx, tcancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer tcancel()
	if err := task.Wait(tctx); err != nil {
		return fmt.Errorf("error waiting for poweroff: %s", err)
	}
	return nil
}

// testGetTagCategory gets a tag category by name.
func testGetTagCategory(s *terraform.State, resourceName string) (*tags.Category, error) {
	tVars, err := testClientVariablesForResource(s, fmt.Sprintf("vsphere_tag_category.%s", resourceName))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer cancel()
	category, err := tVars.tagsClient.GetCategory(ctx, tVars.resourceID)
	if err != nil {
		return nil, fmt.Errorf("could not get tag category for ID %q: %s", tVars.resourceID, err)
	}

	return category, nil
}

// testGetTag gets a tag by name.
func testGetTag(s *terraform.State, resourceName string) (*tags.Tag, error) {
	tVars, err := testClientVariablesForResource(s, fmt.Sprintf("vsphere_tag.%s", resourceName))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer cancel()
	tag, err := tVars.tagsClient.GetTag(ctx, tVars.resourceID)
	if err != nil {
		return nil, fmt.Errorf("could not get tag for ID %q: %s", tVars.resourceID, err)
	}

	return tag, nil
}

// testObjectHasTags checks an object to see if it has the tags that currently
// exist in the Terrafrom state under the resource with the supplied name.
func testObjectHasTags(s *terraform.State, client *tags.RestClient, obj object.Reference, tagResName string) error {
	var expectedIDs []string
	if tagRS, ok := s.RootModule().Resources[fmt.Sprintf("vsphere_tag.%s", tagResName)]; ok {
		expectedIDs = append(expectedIDs, tagRS.Primary.ID)
	} else {
		var n int
		for {
			multiTagRS, ok := s.RootModule().Resources[fmt.Sprintf("vsphere_tag.%s.%d", tagResName, n)]
			if !ok {
				break
			}
			expectedIDs = append(expectedIDs, multiTagRS.Primary.ID)
			n++
		}
	}
	if len(expectedIDs) < 1 {
		return fmt.Errorf("could not find state for vsphere_tag.%s or vsphere_tag.%s.*", tagResName, tagResName)
	}

	objID := obj.Reference().Value
	objType, err := tagTypeForObject(obj)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer cancel()
	actualIDs, err := client.ListAttachedTags(ctx, objID, objType)
	if err != nil {
		return err
	}

	for _, expectedID := range expectedIDs {
		var found bool
		for _, actualID := range actualIDs {
			if expectedID == actualID {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("could not find expected tag ID %q attached to object %q", expectedID, obj.Reference().Value)
		}
	}

	return nil
}
