/*
Copyright 2016 The Rook Authors. All rights reserved.

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

// Package osd for the Ceph OSDs.
package osd

import (
	"testing"

	cephv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	rookv1 "github.com/rook/rook/pkg/apis/rook.io/v1"
	"github.com/rook/rook/pkg/clusterd"
	"github.com/rook/rook/pkg/daemon/ceph/client"
	cephclient "github.com/rook/rook/pkg/daemon/ceph/client"
	"github.com/rook/rook/pkg/operator/ceph/cluster/osd/config"
	opconfig "github.com/rook/rook/pkg/operator/ceph/config"
	cephver "github.com/rook/rook/pkg/operator/ceph/version"
	exectest "github.com/rook/rook/pkg/util/exec/test"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPodContainer(t *testing.T) {
	cluster := &Cluster{rookVersion: "23", clusterInfo: client.AdminClusterInfo("myosd")}
	osdProps := osdProperties{
		crushHostname: "node",
		devices:       []rookv1.Device{},
		resources:     v1.ResourceRequirements{},
		storeConfig:   config.StoreConfig{},
		schedulerName: "custom-scheduler",
	}
	dataPathMap := &provisionConfig{
		DataPathMap: opconfig.NewDatalessDaemonDataPathMap(cluster.clusterInfo.Namespace, "/var/lib/rook"),
	}
	c, err := cluster.provisionPodTemplateSpec(osdProps, v1.RestartPolicyAlways, dataPathMap)
	assert.NotNil(t, c)
	assert.Nil(t, err)
	assert.Equal(t, 1, len(c.Spec.InitContainers))
	assert.Equal(t, 1, len(c.Spec.Containers))
	assert.Equal(t, "custom-scheduler", c.Spec.SchedulerName)
	container := c.Spec.InitContainers[0]
	logger.Infof("container: %+v", container)
	assert.Equal(t, "copy-binaries", container.Args[0])
	container = c.Spec.Containers[0]
	assert.Equal(t, "/rook/tini", container.Command[0])
	assert.Equal(t, "--", container.Args[0])
	assert.Equal(t, "/rook/rook", container.Args[1])
	assert.Equal(t, "ceph", container.Args[2])
	assert.Equal(t, "osd", container.Args[3])
	assert.Equal(t, "provision", container.Args[4])
	findDuplicateEnvVars(t, c.Spec)
}

func TestDaemonset(t *testing.T) {
	testPodDevices(t, "", "sda", true)
	testPodDevices(t, "/var/lib/mydatadir", "sdb", false)
	testPodDevices(t, "", "", true)
	testPodDevices(t, "", "", false)
}

func testPodDevices(t *testing.T, dataDir, deviceName string, allDevices bool) {
	devices := []rookv1.Device{
		{Name: deviceName},
	}

	clientset := fake.NewSimpleClientset()
	clusterInfo := &cephclient.ClusterInfo{
		Namespace:   "ns",
		CephVersion: cephver.Nautilus,
	}
	context := &clusterd.Context{Clientset: clientset, ConfigDir: "/var/lib/rook", Executor: &exectest.MockExecutor{}}
	spec := cephv1.ClusterSpec{
		CephVersion: cephv1.CephVersionSpec{Image: "ceph/ceph:v15"},
		Storage: rookv1.StorageScopeSpec{
			Selection: rookv1.Selection{UseAllDevices: &allDevices, DeviceFilter: deviceName},
			Nodes:     []rookv1.Node{{Name: "node1"}},
		},
		PriorityClassNames: map[rookv1.KeyType]string{
			cephv1.KeyOSD: "my-priority-class",
		},
	}
	c := New(context, clusterInfo, spec, "rook/rook:myversion")

	devMountNeeded := deviceName != "" || allDevices

	n := c.spec.Storage.ResolveNode(spec.Storage.Nodes[0].Name)
	if len(devices) == 0 && len(dataDir) == 0 {
		return
	}
	osd := OSDInfo{
		ID: 0,
	}

	osdProp := osdProperties{
		crushHostname: n.Name,
		selection:     n.Selection,
		resources:     v1.ResourceRequirements{},
		storeConfig:   config.StoreConfig{},
		schedulerName: "custom-scheduler",
	}

	dataPathMap := &provisionConfig{
		DataPathMap: opconfig.NewDatalessDaemonDataPathMap(c.clusterInfo.Namespace, "/var/lib/rook"),
	}

	// Test LVM based on OSD on bare metal
	deployment, err := c.makeDeployment(osdProp, osd, dataPathMap)
	assert.Nil(t, err)
	assert.NotNil(t, deployment)
	assert.Equal(t, "rook-ceph-osd-0", deployment.Name)
	assert.Equal(t, c.clusterInfo.Namespace, deployment.Namespace)
	assert.Equal(t, serviceAccountName, deployment.Spec.Template.Spec.ServiceAccountName)
	assert.Equal(t, int32(1), *(deployment.Spec.Replicas))
	assert.Equal(t, "node1", deployment.Spec.Template.Spec.NodeSelector[v1.LabelHostname])
	assert.Equal(t, v1.RestartPolicyAlways, deployment.Spec.Template.Spec.RestartPolicy)
	assert.Equal(t, "my-priority-class", deployment.Spec.Template.Spec.PriorityClassName)
	if devMountNeeded && len(dataDir) > 0 {
		assert.Equal(t, 7, len(deployment.Spec.Template.Spec.Volumes))
	}
	if devMountNeeded && len(dataDir) == 0 {
		assert.Equal(t, 7, len(deployment.Spec.Template.Spec.Volumes))
	}
	if !devMountNeeded && len(dataDir) > 0 {
		assert.Equal(t, 1, len(deployment.Spec.Template.Spec.Volumes))
	}
	assert.Equal(t, "custom-scheduler", deployment.Spec.Template.Spec.SchedulerName)

	assert.Equal(t, "rook-data", deployment.Spec.Template.Spec.Volumes[0].Name)

	assert.Equal(t, AppName, deployment.Spec.Template.ObjectMeta.Name)
	assert.Equal(t, AppName, deployment.Spec.Template.ObjectMeta.Labels["app"])
	assert.Equal(t, c.clusterInfo.Namespace, deployment.Spec.Template.ObjectMeta.Labels["rook_cluster"])
	assert.Equal(t, 0, len(deployment.Spec.Template.ObjectMeta.Annotations))

	assert.Equal(t, 2, len(deployment.Spec.Template.Spec.InitContainers))
	initCont := deployment.Spec.Template.Spec.InitContainers[0]
	assert.Equal(t, "ceph/ceph:v15", initCont.Image)
	assert.Equal(t, "activate", initCont.Name)
	assert.Equal(t, 3, len(initCont.VolumeMounts))

	assert.Equal(t, 1, len(deployment.Spec.Template.Spec.Containers))
	cont := deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, spec.CephVersion.Image, cont.Image)
	assert.Equal(t, 7, len(cont.VolumeMounts))
	assert.Equal(t, "ceph-osd", cont.Command[0])

	// Test OSD on PVC with LVM
	osdProp = osdProperties{
		crushHostname: n.Name,
		selection:     n.Selection,
		resources:     v1.ResourceRequirements{},
		storeConfig:   config.StoreConfig{},
		pvc:           v1.PersistentVolumeClaimVolumeSource{ClaimName: "mypvc"},
	}
	// Not needed when running on PVC
	osd = OSDInfo{
		ID:     0,
		CVMode: "lvm",
	}

	deployment, err = c.makeDeployment(osdProp, osd, dataPathMap)
	assert.Nil(t, err)
	assert.NotNil(t, deployment)
	assert.Equal(t, 4, len(deployment.Spec.Template.Spec.InitContainers), deployment.Spec.Template.Spec.InitContainers[2].Name)
	assert.Equal(t, "config-init", deployment.Spec.Template.Spec.InitContainers[0].Name)
	assert.Equal(t, "copy-bins", deployment.Spec.Template.Spec.InitContainers[1].Name)
	assert.Equal(t, "blkdevmapper", deployment.Spec.Template.Spec.InitContainers[2].Name)
	assert.Equal(t, "chown-container-data-dir", deployment.Spec.Template.Spec.InitContainers[3].Name)
	assert.Equal(t, 1, len(deployment.Spec.Template.Spec.Containers))
	initCont = deployment.Spec.Template.Spec.InitContainers[0]
	assert.Equal(t, 4, len(initCont.VolumeMounts), initCont.VolumeMounts)
	blkInitCont := deployment.Spec.Template.Spec.InitContainers[2]
	assert.Equal(t, 1, len(blkInitCont.VolumeDevices))
	cont = deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, 8, len(cont.VolumeMounts), cont.VolumeMounts)

	// Test OSD on PVC with RAW
	osd = OSDInfo{
		ID:     0,
		CVMode: "raw",
	}
	deployment, err = c.makeDeployment(osdProp, osd, dataPathMap)
	assert.Nil(t, err)
	assert.NotNil(t, deployment)
	assert.Equal(t, 4, len(deployment.Spec.Template.Spec.InitContainers), deployment.Spec.Template.Spec.InitContainers[2].Name)
	assert.Equal(t, "blkdevmapper", deployment.Spec.Template.Spec.InitContainers[0].Name)
	assert.Equal(t, "activate", deployment.Spec.Template.Spec.InitContainers[1].Name)
	assert.Equal(t, "expand-bluefs", deployment.Spec.Template.Spec.InitContainers[2].Name)
	assert.Equal(t, "chown-container-data-dir", deployment.Spec.Template.Spec.InitContainers[3].Name)
	assert.Equal(t, 1, len(deployment.Spec.Template.Spec.Containers))
	cont = deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, 6, len(cont.VolumeMounts), cont.VolumeMounts)

	// Test with encrypted OSD on PVC with RAW
	osdProp.encrypted = true
	deployment, err = c.makeDeployment(osdProp, osd, dataPathMap)
	assert.Nil(t, err)
	assert.NotNil(t, deployment)
	assert.Equal(t, 7, len(deployment.Spec.Template.Spec.InitContainers), deployment.Spec.Template.Spec.InitContainers[2].Name)
	assert.Equal(t, "encryption-open", deployment.Spec.Template.Spec.InitContainers[0].Name)
	assert.Equal(t, "blkdevmapper-encryption", deployment.Spec.Template.Spec.InitContainers[1].Name)
	assert.Equal(t, "encrypted-block-status", deployment.Spec.Template.Spec.InitContainers[2].Name)
	assert.Equal(t, "expand-encrypted-bluefs", deployment.Spec.Template.Spec.InitContainers[3].Name)
	assert.Equal(t, "activate", deployment.Spec.Template.Spec.InitContainers[4].Name)
	assert.Equal(t, "expand-bluefs", deployment.Spec.Template.Spec.InitContainers[5].Name)
	assert.Equal(t, "chown-container-data-dir", deployment.Spec.Template.Spec.InitContainers[6].Name)
	assert.Equal(t, 1, len(deployment.Spec.Template.Spec.Containers))
	cont = deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, 7, len(cont.VolumeMounts), cont.VolumeMounts)
	osdProp.encrypted = false

	// // Test OSD on PVC with RAW and metadata device
	osd = OSDInfo{
		ID:     0,
		CVMode: "raw",
	}
	osdProp.metadataPVC = v1.PersistentVolumeClaimVolumeSource{ClaimName: "mypvc-metadata"}
	deployment, err = c.makeDeployment(osdProp, osd, dataPathMap)
	assert.Nil(t, err)
	assert.NotNil(t, deployment)
	assert.Equal(t, 5, len(deployment.Spec.Template.Spec.InitContainers))
	assert.Equal(t, "blkdevmapper", deployment.Spec.Template.Spec.InitContainers[0].Name)
	assert.Equal(t, "blkdevmapper-metadata", deployment.Spec.Template.Spec.InitContainers[1].Name)
	assert.Equal(t, "activate", deployment.Spec.Template.Spec.InitContainers[2].Name)
	assert.Equal(t, "expand-bluefs", deployment.Spec.Template.Spec.InitContainers[3].Name)
	assert.Equal(t, "chown-container-data-dir", deployment.Spec.Template.Spec.InitContainers[4].Name)
	assert.Equal(t, 1, len(deployment.Spec.Template.Spec.Containers))
	cont = deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, 6, len(cont.VolumeMounts), cont.VolumeMounts)
	blkInitCont = deployment.Spec.Template.Spec.InitContainers[1]
	assert.Equal(t, 1, len(blkInitCont.VolumeDevices))
	blkMetaInitCont := deployment.Spec.Template.Spec.InitContainers[2]
	assert.Equal(t, 1, len(blkMetaInitCont.VolumeDevices))

	// // Test encrypted OSD on PVC with RAW and metadata device
	osd = OSDInfo{
		ID:     0,
		CVMode: "raw",
	}
	osdProp.encrypted = true
	osdProp.metadataPVC = v1.PersistentVolumeClaimVolumeSource{ClaimName: "mypvc-metadata"}
	deployment, err = c.makeDeployment(osdProp, osd, dataPathMap)
	assert.Nil(t, err)
	assert.NotNil(t, deployment)
	assert.Equal(t, 9, len(deployment.Spec.Template.Spec.InitContainers))
	assert.Equal(t, "encryption-open", deployment.Spec.Template.Spec.InitContainers[0].Name)
	assert.Equal(t, "encryption-open-metadata", deployment.Spec.Template.Spec.InitContainers[1].Name)
	assert.Equal(t, "blkdevmapper-encryption", deployment.Spec.Template.Spec.InitContainers[2].Name)
	assert.Equal(t, "blkdevmapper-metadata-encryption", deployment.Spec.Template.Spec.InitContainers[3].Name)
	assert.Equal(t, "encrypted-block-status", deployment.Spec.Template.Spec.InitContainers[4].Name)
	assert.Equal(t, "expand-encrypted-bluefs", deployment.Spec.Template.Spec.InitContainers[5].Name)
	assert.Equal(t, "activate", deployment.Spec.Template.Spec.InitContainers[6].Name)
	assert.Equal(t, "expand-bluefs", deployment.Spec.Template.Spec.InitContainers[7].Name)
	assert.Equal(t, "chown-container-data-dir", deployment.Spec.Template.Spec.InitContainers[8].Name)
	assert.Equal(t, 1, len(deployment.Spec.Template.Spec.Containers))
	cont = deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, 7, len(cont.VolumeMounts), cont.VolumeMounts)
	blkInitCont = deployment.Spec.Template.Spec.InitContainers[1]
	assert.Equal(t, 1, len(blkInitCont.VolumeDevices))
	blkMetaInitCont = deployment.Spec.Template.Spec.InitContainers[6]
	assert.Equal(t, 1, len(blkMetaInitCont.VolumeDevices))
	osdProp.encrypted = false

	// // Test OSD on PVC with RAW / metadata and wal device
	osd = OSDInfo{
		ID:     0,
		CVMode: "raw",
	}
	osdProp.metadataPVC = v1.PersistentVolumeClaimVolumeSource{ClaimName: "mypvc-metadata"}
	osdProp.walPVC = v1.PersistentVolumeClaimVolumeSource{ClaimName: "mypvc-wal"}
	deployment, err = c.makeDeployment(osdProp, osd, dataPathMap)
	assert.Nil(t, err)
	assert.NotNil(t, deployment)
	assert.Equal(t, 6, len(deployment.Spec.Template.Spec.InitContainers))
	assert.Equal(t, "blkdevmapper", deployment.Spec.Template.Spec.InitContainers[0].Name)
	assert.Equal(t, "blkdevmapper-metadata", deployment.Spec.Template.Spec.InitContainers[1].Name)
	assert.Equal(t, "blkdevmapper-wal", deployment.Spec.Template.Spec.InitContainers[2].Name)
	assert.Equal(t, "activate", deployment.Spec.Template.Spec.InitContainers[3].Name)
	assert.Equal(t, "expand-bluefs", deployment.Spec.Template.Spec.InitContainers[4].Name)
	assert.Equal(t, "chown-container-data-dir", deployment.Spec.Template.Spec.InitContainers[5].Name)
	assert.Equal(t, 1, len(deployment.Spec.Template.Spec.Containers))
	cont = deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, 6, len(cont.VolumeMounts), cont.VolumeMounts)
	blkInitCont = deployment.Spec.Template.Spec.InitContainers[1]
	assert.Equal(t, 1, len(blkInitCont.VolumeDevices))
	blkMetaInitCont = deployment.Spec.Template.Spec.InitContainers[2]
	assert.Equal(t, 1, len(blkMetaInitCont.VolumeDevices))

	// // Test encrypted OSD on PVC with RAW / metadata and wal device
	osd = OSDInfo{
		ID:     0,
		CVMode: "raw",
	}
	osdProp.encrypted = true
	osdProp.metadataPVC = v1.PersistentVolumeClaimVolumeSource{ClaimName: "mypvc-metadata"}
	osdProp.walPVC = v1.PersistentVolumeClaimVolumeSource{ClaimName: "mypvc-wal"}
	deployment, err = c.makeDeployment(osdProp, osd, dataPathMap)
	assert.Nil(t, err)
	assert.NotNil(t, deployment)
	assert.Equal(t, 11, len(deployment.Spec.Template.Spec.InitContainers))
	assert.Equal(t, "encryption-open", deployment.Spec.Template.Spec.InitContainers[0].Name)
	assert.Equal(t, "encryption-open-metadata", deployment.Spec.Template.Spec.InitContainers[1].Name)
	assert.Equal(t, "encryption-open-wal", deployment.Spec.Template.Spec.InitContainers[2].Name)
	assert.Equal(t, "blkdevmapper-encryption", deployment.Spec.Template.Spec.InitContainers[3].Name)
	assert.Equal(t, "blkdevmapper-metadata-encryption", deployment.Spec.Template.Spec.InitContainers[4].Name)
	assert.Equal(t, "blkdevmapper-wal-encryption", deployment.Spec.Template.Spec.InitContainers[5].Name)
	assert.Equal(t, "encrypted-block-status", deployment.Spec.Template.Spec.InitContainers[6].Name)
	assert.Equal(t, "expand-encrypted-bluefs", deployment.Spec.Template.Spec.InitContainers[7].Name)
	assert.Equal(t, "activate", deployment.Spec.Template.Spec.InitContainers[8].Name)
	assert.Equal(t, "expand-bluefs", deployment.Spec.Template.Spec.InitContainers[9].Name)
	assert.Equal(t, "chown-container-data-dir", deployment.Spec.Template.Spec.InitContainers[10].Name)
	assert.Equal(t, 1, len(deployment.Spec.Template.Spec.Containers))
	cont = deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, 7, len(cont.VolumeMounts), cont.VolumeMounts)
	blkInitCont = deployment.Spec.Template.Spec.InitContainers[1]
	assert.Equal(t, 1, len(blkInitCont.VolumeDevices))
	blkMetaInitCont = deployment.Spec.Template.Spec.InitContainers[8]
	assert.Equal(t, 1, len(blkMetaInitCont.VolumeDevices))

	// Test tune Fast settings when OSD on PVC
	osdProp.tuneFastDeviceClass = true
	deployment, err = c.makeDeployment(osdProp, osd, dataPathMap)
	assert.NoError(t, err)
	for flag, val := range defaultTuneFastSettings {
		assert.Contains(t, deployment.Spec.Template.Spec.Containers[0].Args, opconfig.NewFlag(flag, val))
	}

	// Test tune Slow settings when OSD on PVC
	osdProp.tuneSlowDeviceClass = true
	deployment, err = c.makeDeployment(osdProp, osd, dataPathMap)
	assert.NoError(t, err)
	for flag, val := range defaultTuneSlowSettings {
		assert.Contains(t, deployment.Spec.Template.Spec.Containers[0].Args, opconfig.NewFlag(flag, val))
	}
}

func verifyEnvVar(t *testing.T, envVars []v1.EnvVar, expectedName, expectedValue string, expectedFound bool) {
	found := false
	for _, envVar := range envVars {
		if envVar.Name == expectedName {
			assert.Equal(t, expectedValue, envVar.Value)
			found = true
			break
		}
	}

	assert.Equal(t, expectedFound, found)
}

func TestStorageSpecConfig(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	clusterInfo := &cephclient.ClusterInfo{
		Namespace:   "ns",
		CephVersion: cephver.Nautilus,
	}
	context := &clusterd.Context{Clientset: clientset, ConfigDir: "/var/lib/rook", Executor: &exectest.MockExecutor{}}
	spec := cephv1.ClusterSpec{
		DataDirHostPath: context.ConfigDir,
		Storage: rookv1.StorageScopeSpec{
			Nodes: []rookv1.Node{
				{
					Name: "node1",
					Config: map[string]string{
						"databaseSizeMB": "10",
						"walSizeMB":      "20",
						"metadataDevice": "nvme093",
					},
					Selection: rookv1.Selection{},
					Resources: v1.ResourceRequirements{
						Limits: v1.ResourceList{
							v1.ResourceCPU:    *resource.NewQuantity(1024.0, resource.BinarySI),
							v1.ResourceMemory: *resource.NewQuantity(4096.0, resource.BinarySI),
						},
						Requests: v1.ResourceList{
							v1.ResourceCPU:    *resource.NewQuantity(500.0, resource.BinarySI),
							v1.ResourceMemory: *resource.NewQuantity(2048.0, resource.BinarySI),
						},
					},
				},
			},
		},
	}

	c := New(context, clusterInfo, spec, "rook/rook:myversion")
	n := c.spec.Storage.ResolveNode(spec.Storage.Nodes[0].Name)
	storeConfig := config.ToStoreConfig(spec.Storage.Nodes[0].Config)
	metadataDevice := config.MetadataDevice(spec.Storage.Nodes[0].Config)

	osdProp := osdProperties{
		crushHostname:  n.Name,
		devices:        n.Devices,
		selection:      n.Selection,
		resources:      c.spec.Storage.Nodes[0].Resources,
		storeConfig:    storeConfig,
		metadataDevice: metadataDevice,
	}

	dataPathMap := &provisionConfig{
		DataPathMap: opconfig.NewDatalessDaemonDataPathMap(c.clusterInfo.Namespace, "/var/lib/rook"),
	}

	job, err := c.makeJob(osdProp, dataPathMap)
	assert.NotNil(t, job)
	assert.Nil(t, err)
	assert.Equal(t, "rook-ceph-osd-prepare-node1", job.ObjectMeta.Name)
	container := job.Spec.Template.Spec.InitContainers[0]
	assert.NotNil(t, container)
	container = job.Spec.Template.Spec.Containers[0]
	assert.NotNil(t, container)
	verifyEnvVar(t, container.Env, "ROOK_OSD_DATABASE_SIZE", "10", true)
	verifyEnvVar(t, container.Env, "ROOK_OSD_WAL_SIZE", "20", true)
	verifyEnvVar(t, container.Env, "ROOK_METADATA_DEVICE", "nvme093", true)
}

func TestHostNetwork(t *testing.T) {
	storageSpec := rookv1.StorageScopeSpec{
		Nodes: []rookv1.Node{
			{
				Name: "node1",
				Config: map[string]string{
					"databaseSizeMB": "10",
					"walSizeMB":      "20",
				},
			},
		},
	}

	clientset := fake.NewSimpleClientset()
	clusterInfo := &cephclient.ClusterInfo{
		Namespace:   "ns",
		CephVersion: cephver.Nautilus,
	}
	context := &clusterd.Context{Clientset: clientset, ConfigDir: "/var/lib/rook", Executor: &exectest.MockExecutor{}}
	spec := cephv1.ClusterSpec{
		Storage: storageSpec,
		Network: cephv1.NetworkSpec{HostNetwork: true},
	}
	c := New(context, clusterInfo, spec, "rook/rook:myversion")

	n := c.spec.Storage.ResolveNode(storageSpec.Nodes[0].Name)
	osd := OSDInfo{
		ID: 0,
	}

	osdProp := osdProperties{
		crushHostname: n.Name,
		devices:       n.Devices,
		selection:     n.Selection,
		resources:     c.spec.Storage.Nodes[0].Resources,
		storeConfig:   config.StoreConfig{},
	}

	dataPathMap := &provisionConfig{
		DataPathMap: opconfig.NewDatalessDaemonDataPathMap(c.clusterInfo.Namespace, "/var/lib/rook"),
	}

	r, err := c.makeDeployment(osdProp, osd, dataPathMap)
	assert.NotNil(t, r)
	assert.Nil(t, err)

	assert.Equal(t, "rook-ceph-osd-0", r.ObjectMeta.Name)
	assert.Equal(t, true, r.Spec.Template.Spec.HostNetwork)
	assert.Equal(t, v1.DNSClusterFirstWithHostNet, r.Spec.Template.Spec.DNSPolicy)
}

func TestOsdPrepareResources(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	context := &clusterd.Context{Clientset: clientset, ConfigDir: "/var/lib/rook", Executor: &exectest.MockExecutor{}}
	clusterInfo := &cephclient.ClusterInfo{Namespace: "ns"}
	spec := cephv1.ClusterSpec{
		Resources: map[string]v1.ResourceRequirements{"prepareosd": {
			Limits: v1.ResourceList{
				v1.ResourceCPU: *resource.NewQuantity(2000.0, resource.BinarySI),
			},
			Requests: v1.ResourceList{
				v1.ResourceMemory: *resource.NewQuantity(250.0, resource.BinarySI),
			},
		},
		},
	}
	c := New(context, clusterInfo, spec, "rook/rook:myversion")

	r := cephv1.GetPrepareOSDResources(c.spec.Resources)
	assert.Equal(t, "2000", r.Limits.Cpu().String())
	assert.Equal(t, "0", r.Requests.Cpu().String())
	assert.Equal(t, "0", r.Limits.Memory().String())
	assert.Equal(t, "250", r.Requests.Memory().String())
}

func TestClusterGetPVCEncryptionOpenInitContainerActivate(t *testing.T) {
	c := New(&clusterd.Context{}, &cephclient.ClusterInfo{}, cephv1.ClusterSpec{}, "rook/rook:myversion")
	osdProperties := osdProperties{
		pvc: v1.PersistentVolumeClaimVolumeSource{
			ClaimName: "pvc1",
		},
	}

	// No metadata PVC
	containers := c.getPVCEncryptionOpenInitContainerActivate(osdProperties)
	assert.Equal(t, 1, len(containers))

	// With metadata PVC
	osdProperties.metadataPVC.ClaimName = "pvcDB"
	containers = c.getPVCEncryptionOpenInitContainerActivate(osdProperties)
	assert.Equal(t, 2, len(containers))

	// With wal PVC
	osdProperties.walPVC.ClaimName = "pvcWal"
	containers = c.getPVCEncryptionOpenInitContainerActivate(osdProperties)
	assert.Equal(t, 3, len(containers))
}

func TestClusterGetPVCEncryptionInitContainerActivate(t *testing.T) {
	c := New(&clusterd.Context{}, &cephclient.ClusterInfo{}, cephv1.ClusterSpec{}, "rook/rook:myversion")
	osdProperties := osdProperties{
		pvc: v1.PersistentVolumeClaimVolumeSource{
			ClaimName: "pvc1",
		},
		resources: v1.ResourceRequirements{},
	}
	mountPath := "/var/lib/ceph/osd/ceph-0"

	// No metadata PVC
	containers := c.getPVCEncryptionInitContainerActivate(mountPath, osdProperties)
	assert.Equal(t, 1, len(containers))

	// With metadata PVC
	osdProperties.metadataPVC.ClaimName = "pvcDB"
	containers = c.getPVCEncryptionInitContainerActivate(mountPath, osdProperties)
	assert.Equal(t, 2, len(containers))

	// With wal PVC
	osdProperties.walPVC.ClaimName = "pvcWal"
	containers = c.getPVCEncryptionInitContainerActivate(mountPath, osdProperties)
	assert.Equal(t, 3, len(containers))
}
