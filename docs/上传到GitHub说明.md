# 将当前项目上传到 GitHub

目标仓库：**https://github.com/qq555565/Seer-golang**

## 一、安装 Git（若尚未安装）

1. 打开 https://git-scm.com/download/win 下载并安装。
2. 安装时勾选 **“Add Git to PATH”**。
3. 安装完成后**重新打开**终端 / Cursor。

## 二、在 GitHub 上准备仓库

- 在 https://github.com/qq555565 下创建仓库 **Seer-golang**（若尚未创建）。
- 不要勾选 “Add a README”等初始化选项，保持空仓库。

## 三、上传方式

### 方式 A：用脚本（推荐）

在项目根目录 `d:\go` 下打开 PowerShell，执行：

```powershell
.\upload-to-github.ps1
```

按提示确认文件后，执行：

```powershell
.\upload-to-github-push.ps1
```

### 方式 B：手动命令

在项目根目录 `d:\go` 下执行：

```powershell
git init
git remote add origin https://github.com/qq555565/Seer-golang.git
git add .
git commit -m "Initial commit: Seer golang project"
git branch -M main
git push -u origin main
```

## 四、认证说明

- **HTTPS**：首次 `git push` 时会提示输入 GitHub 用户名和密码；密码需使用 **Personal Access Token**（在 GitHub → Settings → Developer settings → Personal access tokens 创建）。
- **SSH**：若已配置 SSH 密钥，可将远程地址改为 `git@github.com:qq555565/Seer-golang.git` 后再推送。

## 五、已忽略的文件

`.gitignore` 已配置，以下内容不会上传：

- 构建产物：`*.exe`、`output/`、`.go-build/` 等  
- 缓存：`.cache/`、`.gocache/` 等  
- 压缩包：`*.rar`、`*.zip`  
- 编辑器/系统临时文件  

如需排除更多文件（如 `users.json.imported`），可编辑项目根目录下的 `.gitignore`。
