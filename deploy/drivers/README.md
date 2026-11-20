# CUPS 驱动说明

CloudPrint 按设备上报的型号自动匹配已安装的 PPD（默认递归扫描
`/usr/share/cups/model`、`/usr/share/ppd` 等目录）。加驱动不需要改代码。

镜像构建时会自动执行 `deploy/install-pantum-driver.sh` 安装奔图官方驱动；
其他品牌用下面的脚本或系统包管理器装入即可。

## 奔图（Pantum）

官方 Linux 驱动包里同时包含 PPD 和 CUPS 过滤器。只复制 PPD 不够，
过滤器也必须放到 CUPS 的 filter 目录，否则 `lp` 会接受任务但出不来纸。

```bash
# 在任意 Linux 机器上解压官方包
unzip "Pantum Linux Driver V1_1_186.zip"
cd Pantum*/Resources
dpkg -x pantum_1.1.186-1_amd64.deb /tmp/pantum

# 更省事：直接用仓库里的脚本
sudo ./deploy/install-pantum-driver.sh
```

Docker 镜像会自动安装；若构建时下载失败（例如公司网络限制），可以在容器内补装：

```bash
docker compose exec -u root cloudprint /usr/local/bin/install-pantum-driver.sh
docker compose restart cloudprint
```

官方下载地址（需带 Referer）：
`https://drivers.pantum.com/userfiles/files/download/drive/5103/Pantum%20Linux%20Driver%20V1_1_186.zip`

## 其他品牌

`printer-driver-all` 与 `openprinting-ppds` 已包含在镜像里，惠普、兄弟、
爱普生、佳能等的常见机型可以直接匹配。厂商专有驱动同样把 PPD 放进
`/usr/share/cups/model/<品牌>/`、过滤器放进 `/usr/lib/cups/filter/`，
然后重启 cupsd。
