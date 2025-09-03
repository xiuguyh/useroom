package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// 配置参数
type Config struct {
	memTermPercent   float64  // 内存终止阈值(%)
	memKillPercent   float64  // 内存强制终止阈值(%)
	swapTermPercent  float64  // 交换空间终止阈值(%)
	swapKillPercent  float64  // 交换空间强制终止阈值(%)
	interval         int      // 检查间隔(秒)
	dryRun           bool     // 干跑模式(不实际终止进程)
	debug            bool     // 调试模式
	sortByRSS        bool     // 按RSS排序而非OOM分数
	ignoreSwap       bool     // 忽略交换空间检查
	protectProcesses []string // 受保护的进程名列表，不被终止
}

// 系统内存信息
type MemInfo struct {
	memTotal     uint64
	memAvailable uint64
	swapTotal    uint64
	swapFree     uint64
}

// 进程信息
type Process struct {
	pid      int
	oomScore int
	rss      uint64 // KiB
	comm     string
	cmdline  string
	uid      int
	isZombie bool
}

func main() {
	// 解析命令行参数
	cfg := parseFlags()

	// 主循环
	for {
		// 获取系统内存信息
		memInfo, err := getMemInfo()
		if err != nil {
			log.Fatalf("获取内存信息失败: %v", err)
		}

		// 计算内存和交换空间使用率
		memAvailPercent := float64(memInfo.memAvailable) / float64(memInfo.memTotal) * 100
		swapFreePercent := 100.0
		if memInfo.swapTotal > 0 {
			swapFreePercent = float64(memInfo.swapFree) / float64(memInfo.swapTotal) * 100
		}

		if cfg.debug {
			log.Printf(
				"内存: 总=%dMiB, 可用=%dMiB(%.1f%%); 交换: 总=%dMiB, 空闲=%dMiB(%.1f%%)",
				memInfo.memTotal/1024,
				memInfo.memAvailable/1024,
				memAvailPercent,
				memInfo.swapTotal/1024,
				memInfo.swapFree/1024,
				swapFreePercent,
			)

			if len(cfg.protectProcesses) > 0 {
				log.Printf("受保护的进程: %v", cfg.protectProcesses)
			}
		}

		// 检查是否需要采取行动
		needTerm := memAvailPercent <= cfg.memTermPercent ||
			(!cfg.ignoreSwap && memInfo.swapTotal > 0 && swapFreePercent <= cfg.swapTermPercent)

		needKill := memAvailPercent <= cfg.memKillPercent ||
			(!cfg.ignoreSwap && memInfo.swapTotal > 0 && swapFreePercent <= cfg.swapKillPercent)

		if needTerm || needKill {
			// 获取所有进程信息
			processes, err := getProcesses()
			if err != nil {
				log.Printf("获取进程信息失败: %v", err)
				time.Sleep(time.Duration(cfg.interval) * time.Second)
				continue
			}

			// 筛选非僵尸进程和不受保护的进程
			filtered := filterProcesses(processes, cfg.protectProcesses)
			if len(filtered) == 0 {
				log.Println("没有可终止的进程")
				time.Sleep(time.Duration(cfg.interval) * time.Second)
				continue
			}

			// 选择要终止的进程
			victim := selectVictim(filtered, cfg.sortByRSS)

			// 执行终止操作
			signal := syscall.SIGTERM
			if needKill {
				signal = syscall.SIGKILL
			}

			log.Printf(
				"将终止进程: pid=%d, 名称=%s, OOM分数=%d, 内存占用=%dMiB, 信号=%s",
				victim.pid,
				victim.comm,
				victim.oomScore,
				victim.rss/1024,
				signalString(signal),
			)

			if !cfg.dryRun {
				if err := killProcess(victim.pid, signal); err != nil {
					log.Printf("终止进程失败: %v", err)
				} else {
					log.Printf("进程 %d 已被终止", victim.pid)
				}
			}
		}

		time.Sleep(time.Duration(cfg.interval) * time.Second)
	}
}

// 解析命令行参数
func parseFlags() Config {
	memTerm := flag.Float64("m", 10, "内存终止阈值(%)")
	memKill := flag.Float64("mk", 5, "内存强制终止阈值(%)")
	swapTerm := flag.Float64("s", 10, "交换空间终止阈值(%)")
	swapKill := flag.Float64("sk", 5, "交换空间强制终止阈值(%)")
	interval := flag.Int("i", 1, "检查间隔(秒)")
	dryRun := flag.Bool("dry-run", false, "干跑模式(不实际终止进程)")
	debug := flag.Bool("debug", false, "调试模式")
	sortByRSS := flag.Bool("sort-by-rss", false, "按RSS排序而非OOM分数")
	ignoreSwap := flag.Bool("ignore-swap", false, "忽略交换空间检查")
	protect := flag.String("protect", "", "受保护的进程名，用逗号分隔，不被终止 (例如: -protect 'sshd,nginx')")

	flag.Parse()

	// 解析受保护的进程名列表
	var protectProcesses []string
	if *protect != "" {
		// 分割并清理进程名
		parts := strings.Split(*protect, ",")
		for _, p := range parts {
			trimmed := strings.TrimSpace(p)
			if trimmed != "" {
				protectProcesses = append(protectProcesses, trimmed)
			}
		}
	}

	return Config{
		memTermPercent:   *memTerm,
		memKillPercent:   *memKill,
		swapTermPercent:  *swapTerm,
		swapKillPercent:  *swapKill,
		interval:         *interval,
		dryRun:           *dryRun,
		debug:            *debug,
		sortByRSS:        *sortByRSS,
		ignoreSwap:       *ignoreSwap,
		protectProcesses: protectProcesses,
	}
}

// 获取系统内存信息
func getMemInfo() (MemInfo, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return MemInfo{}, err
	}
	defer file.Close()

	var memInfo MemInfo
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		switch parts[0] {
		case "MemTotal:":
			memInfo.memTotal, _ = strconv.ParseUint(parts[1], 10, 64)
		case "MemAvailable:":
			memInfo.memAvailable, _ = strconv.ParseUint(parts[1], 10, 64)
		case "SwapTotal:":
			memInfo.swapTotal, _ = strconv.ParseUint(parts[1], 10, 64)
		case "SwapFree:":
			memInfo.swapFree, _ = strconv.ParseUint(parts[1], 10, 64)
		}
	}

	if err := scanner.Err(); err != nil {
		return MemInfo{}, err
	}

	return memInfo, nil
}

// 获取所有进程信息
func getProcesses() ([]Process, error) {
	var processes []Process

	// 遍历/proc目录下的所有进程ID
	pids, err := filepath.Glob("/proc/[0-9]*")
	if err != nil {
		return nil, err
	}

	for _, pidPath := range pids {
		pidStr := filepath.Base(pidPath)
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}

		// 获取进程基本信息
		proc, err := getProcessInfo(pid)
		if err != nil {
			if os.IsPermission(err) || os.IsNotExist(err) {
				continue // 忽略无权限或已结束的进程
			}
			log.Printf("获取进程 %d 信息失败: %v", pid, err)
			continue
		}

		processes = append(processes, proc)
	}

	return processes, nil
}

// 获取单个进程信息
func getProcessInfo(pid int) (Process, error) {
	var proc Process
	proc.pid = pid

	// 获取OOM分数
	oomScore, err := readIntFile(fmt.Sprintf("/proc/%d/oom_score", pid))
	if err != nil {
		return proc, err
	}
	proc.oomScore = oomScore

	// 获取RSS内存
	statm, err := readStatm(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		return proc, err
	}
	proc.rss = statm.rss * uint64(getPageSize())

	// 获取进程名称
	comm, err := readComm(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return proc, err
	}
	proc.comm = comm

	// 获取命令行
	cmdline, err := readCmdline(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return proc, err
	}
	proc.cmdline = cmdline

	// 获取进程状态和UID
	stat, err := readStat(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return proc, err
	}
	proc.isZombie = stat.state == 'Z'
	proc.uid = stat.uid

	return proc, nil
}

// 筛选进程(排除僵尸进程、系统关键进程和受保护进程)
func filterProcesses(processes []Process, protected []string) []Process {
	var filtered []Process

	// 创建保护进程的映射，方便查找
	protectedMap := make(map[string]bool)
	for _, p := range protected {
		protectedMap[strings.ToLower(p)] = true
	}

	for _, p := range processes {
		// 排除僵尸进程
		if p.isZombie {
			continue
		}

		// 排除init进程和自身
		if p.pid == 1 || p.pid == os.Getpid() {
			continue
		}

		// 排除受保护的进程
		if protectedMap[strings.ToLower(p.comm)] {
			if len(protected) > 0 {
				log.Printf("保护进程不被终止: pid=%d, 名称=%s", p.pid, p.comm)
			}
			continue
		}

		filtered = append(filtered, p)
	}

	return filtered
}

// 选择要终止的进程
func selectVictim(processes []Process, sortByRSS bool) Process {
	if sortByRSS {
		// 按RSS降序排序
		sort.Slice(processes, func(i, j int) bool {
			return processes[i].rss > processes[j].rss
		})
	} else {
		// 按OOM分数降序排序
		sort.Slice(processes, func(i, j int) bool {
			return processes[i].oomScore > processes[j].oomScore
		})
	}

	return processes[0]
}

// 终止进程
func killProcess(pid int, signal syscall.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	return process.Signal(signal)
}

// 辅助函数: 读取整数文件
func readIntFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// 辅助函数: 读取进程状态文件(statm)
type statm struct {
	size  uint64 // 总程序大小
	rss   uint64 // 驻留集大小(页)
	share uint64
	text  uint64
	lib   uint64
	data  uint64
	dt    uint64
}

func readStatm(path string) (statm, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return statm{}, err
	}

	parts := strings.Fields(string(data))
	if len(parts) < 7 {
		return statm{}, fmt.Errorf("无效的statm格式")
	}

	var s statm
	s.size, _ = strconv.ParseUint(parts[0], 10, 64)
	s.rss, _ = strconv.ParseUint(parts[1], 10, 64)
	s.share, _ = strconv.ParseUint(parts[2], 10, 64)
	s.text, _ = strconv.ParseUint(parts[3], 10, 64)
	s.lib, _ = strconv.ParseUint(parts[4], 10, 64)
	s.data, _ = strconv.ParseUint(parts[5], 10, 64)
	s.dt, _ = strconv.ParseUint(parts[6], 10, 64)

	return s, nil
}

// 辅助函数: 获取页面大小(字节)
func getPageSize() int {
	return syscall.Getpagesize()
}

// 辅助函数: 读取进程名称
func readComm(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}

// 辅助函数: 读取命令行
func readCmdline(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	// 命令行参数以null分隔
	parts := strings.Split(string(data), "\x00")
	return strings.Join(parts[:len(parts)-1], " "), nil
}

// 辅助函数: 读取进程状态文件(stat)
type stat struct {
	pid         int
	comm        string
	state       byte
	ppid        int
	pgrp        int
	session     int
	tty         int
	tpgid       int
	flags       uint32
	minflt      uint64
	cminflt     uint64
	majflt      uint64
	cmajflt     uint64
	utime       uint64
	stime       uint64
	cutime      int64
	cstime      int64
	priority    int32
	nice        int32
	numThreads  int32
	itrealvalue int64
	starttime   uint64
	vsize       uint64
	rss         int64
	uid         int
	gid         int
}

func readStat(path string) (stat, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return stat{}, err
	}

	// 解析stat文件(格式比较特殊，进程名可能包含空格和括号)
	str := string(data)
	firstParen := strings.Index(str, "(")
	lastParen := strings.LastIndex(str, ")")
	if firstParen == -1 || lastParen == -1 {
		return stat{}, fmt.Errorf("无效的stat格式")
	}

	var s stat
	// 解析PID
	pidStr := str[:firstParen]
	s.pid, _ = strconv.Atoi(strings.TrimSpace(pidStr))

	// 解析进程名
	s.comm = str[firstParen+1 : lastParen]

	// 解析剩余字段
	fields := strings.Fields(str[lastParen+2:])
	if len(fields) < 24 {
		return stat{}, fmt.Errorf("无效的stat字段数")
	}

	s.state = fields[0][0]
	s.ppid, _ = strconv.Atoi(fields[1])
	s.pgrp, _ = strconv.Atoi(fields[2])
	s.session, _ = strconv.Atoi(fields[3])
	s.tty, _ = strconv.Atoi(fields[4])
	s.tpgid, _ = strconv.Atoi(fields[5])

	// 修复uint64到uint32的转换
	flagsVal, _ := strconv.ParseUint(fields[6], 10, 32)
	s.flags = uint32(flagsVal)

	s.minflt, _ = strconv.ParseUint(fields[7], 10, 64)
	s.cminflt, _ = strconv.ParseUint(fields[8], 10, 64)
	s.majflt, _ = strconv.ParseUint(fields[9], 10, 64)
	s.cmajflt, _ = strconv.ParseUint(fields[10], 10, 64)
	s.utime, _ = strconv.ParseUint(fields[11], 10, 64)
	s.stime, _ = strconv.ParseUint(fields[12], 10, 64)
	s.cutime, _ = strconv.ParseInt(fields[13], 10, 64)
	s.cstime, _ = strconv.ParseInt(fields[14], 10, 64)

	// 修复int64到int32的转换
	priorityVal, _ := strconv.ParseInt(fields[15], 10, 32)
	s.priority = int32(priorityVal)

	niceVal, _ := strconv.ParseInt(fields[16], 10, 32)
	s.nice = int32(niceVal)

	numThreadsVal, _ := strconv.ParseInt(fields[17], 10, 32)
	s.numThreads = int32(numThreadsVal)

	s.itrealvalue, _ = strconv.ParseInt(fields[18], 10, 64)
	s.starttime, _ = strconv.ParseUint(fields[19], 10, 64)
	s.vsize, _ = strconv.ParseUint(fields[20], 10, 64)
	s.rss, _ = strconv.ParseInt(fields[21], 10, 64)

	// 获取UID
	uid, err := getProcessUID(s.pid)
	if err == nil {
		s.uid = uid
	}

	return s, nil
}

// 获取进程的UID
func getProcessUID(pid int) (int, error) {
	stat, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return 0, err
	}

	if stat, ok := stat.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid), nil
	}

	return 0, fmt.Errorf("无法获取UID")
}

// 信号转字符串
func signalString(signal syscall.Signal) string {
	switch signal {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGKILL:
		return "SIGKILL"
	default:
		return fmt.Sprintf("%d", signal)
	}
}
