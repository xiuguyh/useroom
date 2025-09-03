# UserOOM - 内存监控与进程管理工具

UserOOM 是一个轻量级的内存监控工具，用于在系统内存不足时自动终止占用内存最多的进程，防止系统因内存耗尽而崩溃。

## 问题背景

### Near-OOM 现象

在 Linux 系统中，当内存使用率接近阈值（90%-95%）时，系统并不会立即触发 OOM（Out of Memory）机制来杀死进程。相反，系统会进入一种称为 **Near-OOM** 的状态，此时：

- **内核回收机制**：kswapd 进程开始异步回收内存，对系统影响较小
- **内存水线机制**：当内存低于 min 水线时，内核会阻塞内存分配进程，尝试回收所有可回收内存
- **系统性能影响**：回收过程中涉及文件缓存写入磁盘、遍历内核结构体等操作，导致系统负载飙升、应用被阻塞
- **活锁状态**：内核一边回收文件缓存，应用又不断产生新缓存，使系统持续高负载 even 夯机

### 用户态 OOM 的必要性

传统的内核 OOM 策略在业务延时敏感场景下过于保守，导致：

- **业务抖动**：几百毫秒到数秒的业务延迟
- **系统卡顿**：SSH 无法连接，系统无响应
- **用户体验差**：用户宁愿进程被杀，也不希望系统卡顿

业界解决方案如 Facebook 的 oomd（systemd-oomd）存在以下限制：
- 深度依赖 cgroupV2 和 PSI（Pressure Stall Information）
- 云计算主流仍为 cgroupV1，且 PSI 有性能开销
- 仅支持 cgroup 粒度杀进程，配置复杂

UserOOM 采用用户态提前杀进程的方式，避免系统进入 Near-OOM 状态，具有以下优势：
- 不依赖特定内核特性，兼容性更好
- 支持单进程粒度，配置灵活
- 响应速度快，避免系统卡顿

## 功能特性

- **实时监控**：持续监控系统内存和交换空间使用情况
- **智能终止**：根据 OOM 分数或 RSS 内存使用量选择要终止的进程
- **多级阈值**：支持内存和交换空间的不同阈值设置
- **进程保护**：可配置受保护的进程，避免关键系统进程被终止
- **调试模式**：提供详细的日志输出，便于调试和监控
- **干跑模式**：支持不实际终止进程的测试运行
- **系统服务**：可作为 systemd 服务运行，实现开机自启动

## 安装与编译

### 前置要求

- Go 1.19 或更高版本
- Linux 系统（依赖 /proc 文件系统）
- root 权限（用于终止进程）

### 编译步骤

1. **克隆项目**
   ```bash
   git clone https://github.com/xiuguyh/useroom.git
   cd useroom
   ```

2. **编译二进制文件**
   ```bash
   # 开发环境编译
   go build -o useroom main.go
   
   # 生产环境编译（优化版本）
   go build -ldflags="-s -w" -o useroom main.go
   ```

3. **安装到系统路径**
   ```bash
   sudo cp useroom /usr/local/bin/
   sudo chmod +x /usr/local/bin/useroom
   ```

## 使用说明

### 命令行参数

```bash
useroom [选项]
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-m` | 10 | 内存终止阈值（百分比，0-100） |
| `-mk` | 5 | 内存强制终止阈值（百分比，0-100） |
| `-s` | 10 | 交换空间终止阈值（百分比，0-100） |
| `-sk` | 5 | 交换空间强制终止阈值（百分比，0-100） |
| `-i` | 1 | 检查间隔（秒） |
| `-debug` | false | 启用调试模式，输出详细日志 |
| `-dry-run` | false | 干跑模式，不实际终止进程 |
| `-sort-by-rss` | false | 按RSS排序而非OOM分数 |
| `-ignore-swap` | false | 忽略交换空间检查 |
| `-protect` | "" | 受保护的进程名列表，逗号分隔 |

### 基本使用示例

1. **默认配置运行**
   ```bash
   sudo ./useroom
   ```

2. **调试模式运行**
   ```bash
   sudo ./useroom -debug -dry-run
   ```

3. **自定义阈值运行**
   ```bash
   sudo ./useroom -m 20 -mk 10 -s 20 -sk 10 -i 2
   ```

4. **保护关键进程**
   ```bash
   sudo ./useroom -protect "sshd,nginx,mysql" -debug
   ```

## 配置文件

### systemd 服务配置

UserOOM 提供了 systemd 服务文件，可以方便地设置为系统服务：

1. **复制服务文件**
   ```bash
   sudo cp useroom.service /etc/systemd/system/
   ```

2. **修改服务配置**（可选）
   ```bash
   sudo systemctl edit useroom
   ```
   在打开的编辑器中修改 ExecStart 行，例如：
   ```ini
   [Service]
   ExecStart=/usr/local/bin/useroom -m 15 -mk 10 -protect "systemd,init" -debug
   ```

3. **启用并启动服务**
   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable useroom
   sudo systemctl start useroom
   ```

4. **检查服务状态**
   ```bash
   sudo systemctl status useroom
   ```

### 配置文件详解

#### 服务文件参数说明

**useroom.service** 中的关键配置：

- **MemoryLimit=100M**：限制服务自身内存使用，防止服务本身成为问题
- **Restart=always**：服务异常退出时自动重启
- **RestartSec=5**：重启间隔为5秒
- **TimeoutStartSec=30**：服务启动超时时间为30秒

#### 推荐配置参数

**生产环境推荐配置**：
```bash
# 内存紧张时
/usr/local/bin/useroom -m 15 -mk 5 -s 10 -sk 5 -i 1 -protect "systemd,init,sshd"

# 内存充足时
/usr/local/bin/useroom -m 5 -mk 2 -s 5 -sk 2 -i 2 -protect "systemd,init,sshd,docker"
```

## 日志与监控

### 查看日志

1. **journalctl 查看服务日志**
   ```bash
   sudo journalctl -u useroom -f
   ```

2. **系统日志查看**
   ```bash
   tail -f /var/log/syslog | grep useroom
   ```

### 调试信息

启用 `-debug` 模式后，日志会包含：
- 当前内存使用情况
- 交换空间使用情况
- 受保护的进程列表
- 被终止进程的详细信息
- 信号发送记录

## 工作原理

1. **内存监控**：定期读取 `/proc/meminfo` 获取系统内存状态
2. **进程扫描**：遍历 `/proc` 目录获取所有进程信息
3. **进程筛选**：排除僵尸进程、系统关键进程和受保护进程
4. **进程排序**：根据 OOM 分数或 RSS 内存使用量排序
5. **进程终止**：发送 SIGTERM 或 SIGKILL 信号终止选中的进程

## 故障排除

### 常见问题

1. **权限问题**
   ```bash
   sudo: ./useroom: command not found
   # 解决：确保 useroom 在 PATH 中，或使用完整路径
   ```

2. **服务启动失败**
   ```bash
   sudo systemctl status useroom
   # 检查错误信息并查看日志
   sudo journalctl -u useroom -n 50
   ```

3. **进程未被终止**
   - 检查是否启用了 `-dry-run` 模式
   - 确认进程是否在受保护列表中
   - 查看调试日志确认阈值是否触发

### 性能优化

1. **减少检查频率**：增加 `-i` 参数值
2. **优化排序**：使用 `-sort-by-rss` 替代默认的 OOM 分数排序
3. **限制保护进程**：避免过多进程在保护列表中

## 安全注意事项

- **必须以 root 权限运行**：才能终止其他用户的进程
- **谨慎设置阈值**：过低的阈值可能导致系统进程被意外终止
- **充分测试**：在生产环境使用前，先在测试环境验证配置
- **监控日志**：定期检查日志，确保系统正常运行

## 许可证

本项目采用 MIT 许可证，详见 LICENSE 文件。

## 贡献

欢迎提交 Issue 和 Pull Request 来帮助改进这个项目。

## 相关链接

- [earlyoom 项目](https://github.com/rfjakob/earlyoom) - 原始的 earlyoom 项目
- [systemd OOMD](https://systemd.io/oomd) - systemd 的内存管理守护进程
- [Linux OOM Killer](https://www.kernel.org/doc/gorman/html/understand/understand016.html) - Linux 内核 OOM 机制文档