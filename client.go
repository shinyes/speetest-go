package main

import (
    "encoding/csv"
    "flag"
    "fmt"
    "net"
    "os"
    "os/signal"
    "sync"
    "sync/atomic"
    "syscall"
    "time"
)

var (
    serverAddr  string
    testTime    int
    dataSizeMB  int
    threadCount int
    timeout     = 5 * time.Second
    logFile     *os.File
    csvWriter   *csv.Writer
)

func init() {
    flag.StringVar(&serverAddr, "server", "", "服务端地址（如 127.0.0.1:54312）")
    flag.IntVar(&testTime, "time", 0, "测速时长（秒）")
    flag.IntVar(&dataSizeMB, "size", 0, "测速数据大小（MB）")
    flag.IntVar(&threadCount, "threads", 1, "线程数（默认 1）")
}

type ThreadStats struct {
    sent     int64
    received int64
    latencies []float64
    jitters   []float64
}

func main() {
    flag.Parse()
    if serverAddr == "" || (testTime == 0 && dataSizeMB == 0) {
        fmt.Println("用法示例: ./client -server 127.0.0.1:54312 -time 10 -threads 4")
        fmt.Println("或 ./client -server 127.0.0.1:54312 -size 100 -threads 1")
        return
    }

    // 打开 CSV 文件以追加模式
    var err error
    logFile, err = os.OpenFile("test_results.csv", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
    if err != nil {
        fmt.Println("无法打开日志文件:", err)
        return
    }
    defer logFile.Close()

    // 创建 CSV 写入器
    csvWriter = csv.NewWriter(logFile)
    defer csvWriter.Flush()

    // 如果文件是新创建的，写入 CSV 标题
    fileStat, _ := logFile.Stat()
    if fileStat.Size() == 0 {
        err = csvWriter.Write([]string{"时间", "上传速度(MB/s)", "下载速度(MB/s)", "最小延迟(ms)", "最大延迟(ms)", "平均延迟(ms)", "最小抖动(ms)", "最大抖动(ms)", "平均抖动(ms)"})
        if err != nil {
            fmt.Println("写入 CSV 标题失败:", err)
            return
        }
    }

    fmt.Printf("连接服务端: %s，线程数: %d\n", serverAddr, threadCount)

    var stopFlag int32
    stopChan := make(chan os.Signal, 1)
    signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)
    go func() {
        <-stopChan
        fmt.Println("\n收到中断信号，准备退出...")
        atomic.StoreInt32(&stopFlag, 1)
    }()

    stats := make([]*ThreadStats, threadCount)
    var wg sync.WaitGroup
    startTime := time.Now()

    // 启动实时显示协程
    go func() {
        var lastSent, lastRecv int64
        ticker := time.NewTicker(1 * time.Second)
        defer ticker.Stop()
        for {
            <-ticker.C
            if atomic.LoadInt32(&stopFlag) == 1 {
                break
            }
            var sentNow, recvNow int64
            for _, s := range stats {
                sentNow += atomic.LoadInt64(&s.sent)
                recvNow += atomic.LoadInt64(&s.received)
            }
            uploadRate := float64(sentNow-lastSent) / (1024 * 1024)
            downloadRate := float64(recvNow-lastRecv) / (1024 * 1024)
            fmt.Printf("实时速度 → 上传: %.2f MB/s | 下载: %.2f MB/s\n", uploadRate, downloadRate)
            lastSent = sentNow
            lastRecv = recvNow
        }
    }()

    duration := time.Duration(testTime) * time.Second
    totalBytesToSend := int64(dataSizeMB) * 1024 * 1024

    // 启动多个线程测速
    for i := 0; i < threadCount; i++ {
        wg.Add(1)
        stats[i] = &ThreadStats{}
        go func(s *ThreadStats) {
            defer wg.Done()
            conn, err := net.DialTimeout("tcp", serverAddr, timeout)
            if err != nil {
                fmt.Println("连接服务端失败:", err)
                return
            }
            defer conn.Close()
            buffer := make([]byte, 32*1024)
            var lastRTT float64

            for {
                if atomic.LoadInt32(&stopFlag) == 1 {
                    break
                }

                rttStart := time.Now()
                _, err := conn.Write(buffer)
                if err != nil {
                    break
                }
                atomic.AddInt64(&s.sent, int64(len(buffer)))

                n, err := conn.Read(buffer)
                if err != nil {
                    break
                }
                atomic.AddInt64(&s.received, int64(n))

                rtt := time.Since(rttStart).Seconds() * 1000
                s.latencies = append(s.latencies, rtt)
                if lastRTT > 0 {
                    jitter := absFloat64(rtt - lastRTT)
                    s.jitters = append(s.jitters, jitter)
                }
                lastRTT = rtt

                // 判断停止条件
                if testTime > 0 && time.Since(startTime) >= duration {
                    break
                }
                if dataSizeMB > 0 && atomic.LoadInt64(&s.sent) >= totalBytesToSend/int64(threadCount) {
                    break
                }
            }
        }(stats[i])
    }

    wg.Wait()
    fmt.Println("\n测速结束")

    // 汇总统计
    var totalSent, totalRecv int64
    var allLatencies, allJitters []float64
    for _, s := range stats {
        totalSent += s.sent
        totalRecv += s.received
        allLatencies = append(allLatencies, s.latencies...)
        allJitters = append(allJitters, s.jitters...)
    }

    elapsed := time.Since(startTime).Seconds()
    uploadMB := float64(totalSent) / (1024 * 1024)
    downloadMB := float64(totalRecv) / (1024 * 1024)

    minL, maxL, avgL := calcStats(allLatencies)
    minJ, maxJ, avgJ := calcStats(allJitters)

    // 输出汇总信息
    result := fmt.Sprintf("平均上传速度: %.2f MB/s\n平均下载速度: %.2f MB/s\n", uploadMB/elapsed, downloadMB/elapsed)
    result += fmt.Sprintf("延迟: 最小 %.2f ms，最大 %.2f ms，平均 %.2f ms\n", minL, maxL, avgL)
    result += fmt.Sprintf("抖动: 最小 %.2f ms，最大 %.2f ms，平均 %.2f ms\n", minJ, maxJ, avgJ)

    // 输出到终端
    fmt.Println(result)

    // 写入 CSV
    err = csvWriter.Write([]string{
        time.Now().Format(time.RFC3339),
        fmt.Sprintf("%.2f", uploadMB/elapsed),
        fmt.Sprintf("%.2f", downloadMB/elapsed),
        fmt.Sprintf("%.2f", minL),
        fmt.Sprintf("%.2f", maxL),
        fmt.Sprintf("%.2f", avgL),
        fmt.Sprintf("%.2f", minJ),
        fmt.Sprintf("%.2f", maxJ),
        fmt.Sprintf("%.2f", avgJ),
    })
    if err != nil {
        fmt.Println("写入 CSV 数据失败:", err)
    }
}

func absFloat64(a float64) float64 {
    if a < 0 {
        return -a
    }
    return a
}

func calcStats(data []float64) (min, max, avg float64) {
    min = data[0]
    max = data[0]
    sum := 0.0
    for _, v := range data {
        if v < min {
            min = v
        }
        if v > max {
            max = v
        }
        sum += v
    }
    avg = sum / float64(len(data))
    return
}
