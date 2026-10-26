//go:build windows

package main

// Подсистема "info" (ssh -s info): JSON-сводка о машине целиком из
// Win32 API и реестра — без cmd.exe, WMI и спавнов.

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"syscall"
	"unsafe"

	ssh "github.com/gliderlabs/ssh"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type diskInfo struct {
	Drive   string  `json:"drive"`
	FreeGB  float64 `json:"free_gb"`
	TotalGB float64 `json:"total_gb"`
}

type adapterInfo struct {
	Name string   `json:"name"`
	MAC  string   `json:"mac"`
	IPv4 []string `json:"ipv4"`
}

type progInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type avInfo struct {
	Product string `json:"product"`
	State   string `json:"state"`
}

type sysInfo struct {
	Installed    []progInfo    `json:"installed,omitempty"`
	Antivirus    *avInfo       `json:"antivirus,omitempty"`
	Host         string        `json:"host"`
	Model        string        `json:"model"`
	Manufacturer string        `json:"manufacturer"`
	BIOS         string        `json:"bios"`
	GUID         string        `json:"guid"`
	CPU          string        `json:"cpu"`
	RAMGB        float64       `json:"ram_gb"`
	User         string        `json:"user"`
	SessionID    int           `json:"session_id"`
	Disks        []diskInfo    `json:"disks"`
	Adapters     []adapterInfo `json:"adapters"`
}

func regStr(sub, name string) string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, sub, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue(name)
	return v
}

func infoHandler(s ssh.Session) {
	host, _ := os.Hostname()
	info := sysInfo{Host: host, User: os.Getenv("USERDOMAIN") + "\\" + os.Getenv("USERNAME")}

	const bios = `HARDWARE\DESCRIPTION\System\BIOS`
	info.Model = regStr(bios, "SystemProductName")
	info.Manufacturer = regStr(bios, "SystemManufacturer")
	info.BIOS = regStr(bios, "BIOSVersion")
	info.GUID = regStr(`SOFTWARE\Microsoft\Cryptography`, "MachineGuid")
	info.CPU = regStr(`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "ProcessorNameString")

	info.RAMGB = float64(nativeMemoryLoadTotal().TotalPhys) / (1 << 30)
	var sid uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &sid); err == nil {
		info.SessionID = int(sid)
	}
	info.Installed = readInstalledPrograms()
	info.Antivirus = detectAntivirus()

	bits, _ := windows.GetLogicalDrives()
	for d := 0; d < 26; d++ {
		if bits&(1<<uint(d)) == 0 {
			continue
		}
		root, _ := windows.UTF16PtrFromString(string(rune('A'+d)) + ":\\")
		var free, total uint64
		if err := windows.GetDiskFreeSpaceEx(root, &free, &total, new(uint64)); err != nil {
			continue
		}
		info.Disks = append(info.Disks, diskInfo{
			Drive:   string(rune('A'+d)) + ":",
			FreeGB:  float64(free) / (1 << 30),
			TotalGB: float64(total) / (1 << 30),
		})
	}

	var buf []byte
	size := uint32(16 * 1024)
	for attempt := 0; attempt < 3; attempt++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_INET,
			windows.GAA_FLAG_INCLUDE_PREFIX, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			break
		}
	}
	for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); aa != nil; aa = aa.Next {
		if aa.OperStatus != windows.IfOperStatusUp {
			continue
		}
		a := adapterInfo{Name: windows.UTF16PtrToString(aa.FriendlyName)}
		mac := make([]string, 0, aa.PhysicalAddressLength)
		for _, b := range aa.PhysicalAddress[:aa.PhysicalAddressLength] {
			mac = append(mac, fmt.Sprintf("%02x", b))
		}
		a.MAC = strings.Join(mac, ":")
		for ua := aa.FirstUnicastAddress; ua != nil; ua = ua.Next {
			if sa := ua.Address.Sockaddr; sa == nil {
				continue
			}
			rsa := (*syscall.RawSockaddrInet4)(unsafe.Pointer(ua.Address.Sockaddr))
			if rsa.Family != windows.AF_INET {
				continue
			}
			a.IPv4 = append(a.IPv4, fmt.Sprintf("%d.%d.%d.%d", rsa.Addr[0], rsa.Addr[1], rsa.Addr[2], rsa.Addr[3]))
		}
		info.Adapters = append(info.Adapters, a)
	}

	out, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		fmt.Fprintf(s, "info: %v\n", err)
		return
	}
	fmt.Fprintln(s, string(out))
}

// readInstalledPrograms: установленные программы прямым чтением реестра
// (три ветки Uninstall) — без PowerShell и WMI.
func readInstalledPrograms() []progInfo {
	type src struct {
		root registry.Key
		path string
	}
	srcs := []src{
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`},
		{registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
	}
	seen := map[string]bool{}
	var out []progInfo
	for _, s := range srcs {
		k, err := registry.OpenKey(s.root, s.path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		names, _ := k.ReadSubKeyNames(-1)
		k.Close()
		for _, name := range names {
			sk, err := registry.OpenKey(s.root, s.path+"\\"+name, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			dn, _, _ := sk.GetStringValue("DisplayName")
			ver, _, _ := sk.GetStringValue("DisplayVersion")
			sk.Close()
			dn = strings.TrimSpace(dn)
			if dn == "" || seen[dn] {
				continue
			}
			seen[dn] = true
			out = append(out, progInfo{Name: dn, Version: strings.TrimSpace(ver)})
		}
	}
	return out
}

// detectAntivirus: известные AV-службы + их состояние.
func detectAntivirus() *avInfo {
	markers := []struct {
		re      string
		product string
	}{
		{`(?i)kaspersky|klnagent|avp`, "Kaspersky"},
		{`(?i)msmpeng|defender|mssense`, "Microsoft Defender"},
		{`(?i)drweb`, "Dr.Web"},
		{`(?i)ekrn|eset`, "ESET NOD32"},
		{`(?i)avast`, "Avast"},
		{`(?i)mcafee|mcshield`, "McAfee"},
		{`(?i)symantec|norton`, "Norton/Symantec"},
		{`(?i)bitdefender`, "Bitdefender"},
	}
	for _, svc := range listServices() {
		for _, m := range markers {
			if regexp.MustCompile(m.re).MatchString(svc.name + " " + svc.disp) {
				state := "STOPPED"
				if svc.running {
					state = "RUNNING"
				}
				return &avInfo{Product: m.product, State: state}
			}
		}
	}
	return &avInfo{Product: "не обнаружен", State: "-"}
}
