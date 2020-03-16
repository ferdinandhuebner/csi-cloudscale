/*
Copyright cloudscale.ch
Copyright 2018 DigitalOcean

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

// Code generated by protoc-gen-go. DO NOT EDIT.

// NOTE: THIS IS NOT GENERATED. We have to add the line above to prevent golint
// checking this file. This is needed because some methods end with xxxId, but
// golint wants them to be xxxID. But we're not able to change it as the
// official CSI spec is that way and we have to implement the interface
// exactly.

package driver

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/kubernetes/pkg/util/mount"
	"k8s.io/kubernetes/pkg/util/resizefs"
)

const (
	// TODO we're not sure yet what our limit is, so just use this for now.
	// It's the limit for Google Compute Engine and I don't see what limits
	// this more in OpenStack, except per User Quotas.
	maxVolumesPerNode = 128
)

// NodeStageVolume mounts the volume to a staging path on the node. This is
// called by the CO before NodePublishVolume and is used to temporary mount the
// volume to a staging path. Once mounted, NodePublishVolume will make sure to
// mount it to the appropriate path
func (d *Driver) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	d.log.Info("node stage volume called")
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "NodeStageVolume Volume ID must be provided")
	}

	if req.StagingTargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "NodeStageVolume Staging Target Path must be provided")
	}

	if req.VolumeCapability == nil {
		return nil, status.Error(codes.InvalidArgument, "NodeStageVolume Volume Capability must be provided")
	}

	// Get the first part of the UUID.
	// The linux kernel limits volume serials to 20 bytes:
	// include/uapi/linux/virtio_blk.h:#define VIRTIO_BLK_ID_BYTES 20 /* ID string length */
	linuxSerial := req.VolumeId[:20]
	// Apparently sometimes we need to call udevadm trigger to get the volume
	// properly registered in /dev/disk. More information can be found here:
	// https://github.com/cloudscale-ch/csi-cloudscale/issues/9
	sourcePtr, err := d.mounter.FinalizeVolumeAttachmentAndFindPath(d.log.WithFields(logrus.Fields{"volume_id": req.VolumeId}), linuxSerial)
	if err != nil {
		return nil, err
	}
	source := *sourcePtr

	publishContext := req.GetPublishContext()
	if publishContext == nil {
		return nil, status.Error(codes.InvalidArgument, "PublishContext must be provided")
	}

	volumeName, ok := publishContext[PublishInfoVolumeName]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "Could not find the volume by name")
	}

	luksContext := getLuksContext(req.Secrets, publishContext, VolumeLifecycleNodeStageVolume)

	target := req.StagingTargetPath

	mnt := req.VolumeCapability.GetMount()
	options := mnt.MountFlags

	fsType := "ext4"
	if mnt.FsType != "" {
		fsType = mnt.FsType
	}

	ll := d.log.WithFields(logrus.Fields{
		"volume_id":           req.VolumeId,
		"volume_name":         volumeName,
		"volume_context":      req.VolumeContext,
		"publish_context":     req.PublishContext,
		"staging_target_path": req.StagingTargetPath,
		"source":              source,
		"fsType":              fsType,
		"mount_options":       options,
		"method":              "node_stage_volume",
		"luks_encrypted":      luksContext.EncryptionEnabled,
	})

	formatted, err := d.mounter.IsFormatted(source, luksContext)
	if err != nil {
		return nil, err
	}

	if !formatted {
		ll.Info("formatting the volume for staging")
		if err := d.mounter.Format(source, fsType, luksContext); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	} else {
		ll.Info("source device is already formatted")
	}

	ll.Info("mounting the volume for staging")

	mounted, err := d.mounter.IsMounted(target)
	if err != nil {
		return nil, err
	}

	if !mounted {
		if err := d.mounter.Mount(source, target, fsType, luksContext, options...); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	} else {
		ll.Info("source device is already mounted to the target path")
	}

	ll.Info("formatting and mounting stage volume is finished")
	return &csi.NodeStageVolumeResponse{}, nil
}

// NodeUnstageVolume unstages the volume from the staging path
func (d *Driver) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "NodeUnstageVolume Volume ID must be provided")
	}

	if req.StagingTargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "NodeUnstageVolume Staging Target Path must be provided")
	}

	luksContext := LuksContext{VolumeLifecycle: VolumeLifecycleNodeUnstageVolume}

	ll := d.log.WithFields(logrus.Fields{
		"volume_id":           req.VolumeId,
		"staging_target_path": req.StagingTargetPath,
		"method":              "node_unstage_volume",
	})
	ll.Info("node unstage volume called")

	mounted, err := d.mounter.IsMounted(req.StagingTargetPath)
	if err != nil {
		return nil, err
	}

	if mounted {
		ll.Info("unmounting the staging target path")
		err := d.mounter.Unmount(req.StagingTargetPath, luksContext)
		if err != nil {
			return nil, err
		}
	} else {
		ll.Info("staging target path is already unmounted")
	}

	ll.Info("unmounting stage volume is finished")
	return &csi.NodeUnstageVolumeResponse{}, nil
}

// NodePublishVolume mounts the volume mounted to the staging path to the target path
func (d *Driver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	d.log.Info("node publish volume called")
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "NodePublishVolume Volume ID must be provided")
	}

	if req.StagingTargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "NodePublishVolume Staging Target Path must be provided")
	}

	if req.TargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "NodePublishVolume Target Path must be provided")
	}

	if req.VolumeCapability == nil {
		return nil, status.Error(codes.InvalidArgument, "NodePublishVolume Volume Capability must be provided")
	}

	publishContext := req.GetPublishContext()
	if publishContext == nil {
		return nil, status.Error(codes.InvalidArgument, "PublishContext must be provided")
	}

	luksContext := getLuksContext(req.Secrets, publishContext, VolumeLifecycleNodePublishVolume)

	source := req.StagingTargetPath
	target := req.TargetPath

	mnt := req.VolumeCapability.GetMount()
	options := mnt.MountFlags

	// TODO(arslan): do we need bind here? check it out
	// Perform a bind mount to the full path to allow duplicate mounts of the same PD.
	options = append(options, "bind")
	if req.Readonly {
		options = append(options, "ro")
	}

	fsType := "ext4"
	if mnt.FsType != "" {
		fsType = mnt.FsType
	}

	ll := d.log.WithFields(logrus.Fields{
		"volume_id":      req.VolumeId,
		"source":         source,
		"target":         target,
		"fsType":         fsType,
		"mount_options":  options,
		"method":         "node_publish_volume",
		"luks_encrypted": luksContext.EncryptionEnabled,
	})

	mounted, err := d.mounter.IsMounted(target)
	if err != nil {
		return nil, err
	}

	if !mounted {
		ll.Info("mounting the volume")
		if err := d.mounter.Mount(source, target, fsType, luksContext, options...); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	} else {
		ll.Info("volume is already mounted")
	}

	ll.Info("bind mounting the volume is finished")
	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeUnpublishVolume unmounts the volume from the target path
func (d *Driver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if req.VolumeId == "" {
		return nil, status.Error(codes.InvalidArgument, "NodeUnpublishVolume Volume ID must be provided")
	}

	if req.TargetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "NodeUnpublishVolume Target Path must be provided")
	}

	luksContext := LuksContext{VolumeLifecycle: VolumeLifecycleNodeUnpublishVolume}

	ll := d.log.WithFields(logrus.Fields{
		"volume_id":   req.VolumeId,
		"target_path": req.TargetPath,
		"method":      "node_unpublish_volume",
	})
	ll.Info("node unpublish volume called")

	mounted, err := d.mounter.IsMounted(req.TargetPath)
	if err != nil {
		return nil, err
	}

	if mounted {
		ll.Info("unmounting the target path")
		err := d.mounter.Unmount(req.TargetPath, luksContext)
		if err != nil {
			return nil, err
		}
	} else {
		ll.Info("target path is already unmounted")
	}

	ll.Info("unmounting volume is finished")
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// NodeGetCapabilities returns the supported capabilities of the node server
func (d *Driver) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	nscaps := []*csi.NodeServiceCapability{
		&csi.NodeServiceCapability{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
				},
			},
		},
		&csi.NodeServiceCapability{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
				},
			},
		},
	}

	d.log.WithFields(logrus.Fields{
		"node_capabilities": nscaps,
		"method":            "node_get_capabilities",
	}).Info("node get capabilities called")
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: nscaps,
	}, nil
}

// NodeGetInfo returns the supported capabilities of the node server. This
// should eventually return the droplet ID if possible. This is used so the CO
// knows where to place the workload. The result of this function will be used
// by the CO in ControllerPublishVolume.
func (d *Driver) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	d.log.WithField("method", "node_get_info").Info("node get info called")
	return &csi.NodeGetInfoResponse{
		NodeId:            d.serverId,
		MaxVolumesPerNode: maxVolumesPerNode,

		// make sure that the driver works on this particular region only
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				"region": d.region,
			},
		},
	}, nil
}

// NodeGetVolumeStats returns the volume capacity statistics available for the
// the given volume.
func (d *Driver) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	d.log.WithField("method", "node_get_volume_stats").
		Info("node get volume stats called")

	return nil, status.Error(codes.Unimplemented, "")
}

func (d *Driver) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	volumeID := req.VolumeId
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodeExpandVolume volume ID not provided")
	}

	volumePath := req.VolumePath
	if len(volumePath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodeExpandVolume volume path not provided")
	}

	log := d.log.WithFields(logrus.Fields{
		"volume_id":   req.VolumeId,
		"volume_path": req.VolumePath,
		"method":      "node_expand_volume",
	})
	log.Info("node expand volume called")

	// return we support block volumes and this is a block volume

	mounter := mount.New("")
	devicePath, _, err := mount.GetDeviceNameFromMount(mounter, volumePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "NodeExpandVolume unable to get device path for %q: %v", volumePath, err)
	}

	r := resizefs.NewResizeFs(&mount.SafeFormatAndMount{
		Interface: mounter,
		Exec:      mount.NewOSExec(),
	})

	log = log.WithFields(logrus.Fields{
		"device_path": devicePath,
	})
	log.Info("resizing volume")
	if _, err := r.Resize(devicePath, volumePath); err != nil {
		return nil, status.Errorf(codes.Internal, "NodeExpandVolume could not resize volume %q (%q):  %v", volumeID, req.GetVolumePath(), err)
	}

	log.Info("volume was resized")
	return &csi.NodeExpandVolumeResponse{}, nil
}
