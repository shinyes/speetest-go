# 使用方法

**服务端**
`./server.exe -port 54312`
> 默认监听 54312 端口，可以不指定。

**客户端**
指定时间进行测速：`./client -server 127.0.0.1:54312 -time 10`
> time 单位为秒
指定传输数据进行测速：`./client -server 127.0.0.1:54312 -size 100`
> size 单位为 MB

客户端还可以指定 `- threads 10` 代表10 个线程的多线程测速，数量自定。
