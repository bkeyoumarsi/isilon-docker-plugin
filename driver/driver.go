package driver

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/bkeyoumarsi/isilon-docker-plugin/rest"
	"github.com/calavera/dkvolume"
)

const mountPath = "/var/lib/isilon/volumes/"

type volume struct {
	name        string
	connections int
}

type isiDriver struct {
	clusterPath string
	volumes     map[string]*volume
	restClient  *rest.Client
	m           *sync.Mutex
}

func NewIsilonDriver(addr, usr, pass string) isiDriver {
	d := isiDriver{
		clusterPath: fmt.Sprintf("%s:/ifs/data/docker/volumes/", addr),
		volumes:     map[string]*volume{},
		restClient:  rest.NewClient(addr, usr, pass),
		m:           &sync.Mutex{},
	}
	return d
}

func (d *isiDriver) mountpoint(name string) string {
	return filepath.Join(mountPath, name)
}

func (d isiDriver) Create(req dkvolume.Request) dkvolume.Response {
	log.Printf("Create(%q)", req.Name)
	d.m.Lock()
	defer d.m.Unlock()
	mountpoint := d.mountpoint(req.Name)

	// If volume already in vdb then just return ok
	if _, ok := d.volumes[mountpoint]; ok {
		return dkvolume.Response{}
	}

	exist, err := d.restClient.CheckVolume(req.Name)
	if err != nil {
		log.Printf("Failed to check volume %s existence\n", req.Name)
		return dkvolume.Response{Err: "Failed to create volume"}
	}

	if !exist {
		err = d.restClient.CreateVolume(req.Name)
		if err != nil {
			log.Printf("Failed to create volume %s\n", req.Name)
			return dkvolume.Response{Err: "Failed to create volume"}
		}
	}

	return dkvolume.Response{}
}

func (d isiDriver) Remove(req dkvolume.Request) dkvolume.Response {
	log.Printf("Remove(%q)", req.Name)
	d.m.Lock()
	defer d.m.Unlock()
	mountpoint := d.mountpoint(req.Name)

	if volume, found := d.volumes[mountpoint]; found {
		if volume.connections > 1 {
			log.Printf(
				"Remove(%s) attempted on a volume used by multiple containers\n", req.Name)
			return dkvolume.Response{Err: "Volume in use by other containers"}
		}
		delete(d.volumes, mountpoint)

		err := os.RemoveAll(mountpoint)
		if err != nil {
			log.Printf("Failed to delete volume %s", mountpoint)
			return dkvolume.Response{Err: err.Error()}
		}
	}
	return dkvolume.Response{}
}

func (d isiDriver) Path(req dkvolume.Request) dkvolume.Response {
	return dkvolume.Response{Mountpoint: d.mountpoint(req.Name)}
}

func (d isiDriver) Mount(req dkvolume.Request) dkvolume.Response {
	log.Printf("Mount(%q)", req.Name)
	d.m.Lock()
	defer d.m.Unlock()
	mountpoint := d.mountpoint(req.Name)

	vol, ok := d.volumes[mountpoint]
	if ok && vol.connections > 0 {
		vol.connections++
		return dkvolume.Response{Mountpoint: mountpoint}
	}

	fi, err := os.Lstat(mountpoint)

	if os.IsNotExist(err) {
		if err := os.MkdirAll(mountpoint, 0755); err != nil {
			return dkvolume.Response{Err: err.Error()}
		}
	} else if err != nil {
		return dkvolume.Response{Err: err.Error()}
	}

	if fi != nil && !fi.IsDir() {
		return dkvolume.Response{Err: fmt.Sprintf("%v already exist and it's not a directory", mountpoint)}
	}

	nfsPath := fmt.Sprintf("%s%s", d.clusterPath, req.Name)
	cmd := exec.Command("mount", "-t", "nfs", "-o", "noacl",
		nfsPath, mountpoint)

	err = cmd.Run()
	if err != nil {
		return dkvolume.Response{Err: "Failed to mount volume"}
	}

	d.volumes[mountpoint] = &volume{name: req.Name, connections: 1}

	return dkvolume.Response{Mountpoint: mountpoint}
}

func (d isiDriver) Unmount(req dkvolume.Request) dkvolume.Response {
	log.Printf("Unmount(%q)", req.Name)
	d.m.Lock()
	defer d.m.Unlock()
	mountpoint := d.mountpoint(req.Name)

	if volume, ok := d.volumes[mountpoint]; ok {
		if volume.connections == 1 {
			cmd := exec.Command("umount", mountpoint)
			if err := cmd.Run(); err != nil {
				return dkvolume.Response{Err: "Failed to unmount volume"}
			}
		}
		volume.connections--
	} else {
		return dkvolume.Response{Err: fmt.Sprintf("Unable to find volume mounted on %s", mountpoint)}
	}
	return dkvolume.Response{}
}
