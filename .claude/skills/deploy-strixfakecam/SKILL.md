---
name: deploy-strixcam
description: Deploy StrixCamFake to the test server. Use when the user says "deploy", "deploy to server", "push to server", "update server", or wants to test changes on the remote machine.
---

# Deploy StrixCamFake to server

Target server: `user@10.0.10.51`
Project path on server: `~/StrixCamFake`

## CRITICAL RULES

1. **ALL file transfers go through git.** NEVER use `scp`, `rsync`, or copy files via SSH. Always commit and push first, then `git pull` on the server.
2. **NEVER copy binaries via SSH.** The binary must be built on the server after `git pull`.
3. If there are uncommitted changes locally -- commit and push them FIRST before deploying.

## Deploy steps

### Step 1: Commit and push local changes

Check for uncommitted changes. If any exist, stage, commit, and push them:

```bash
cd /home/user/StrixCamFake
git status
git add -A
git commit -m "<meaningful commit message>"
git push origin main
```

Do NOT push if there are no changes (git status is clean).

### Step 2: Pull and build on server

```bash
ssh user@10.0.10.51 'cd ~/StrixCamFake && git pull && go build -o strixcamfake . && echo "BUILD OK"'
```

If build fails -- fix locally, commit, push, repeat.

### Step 3: Restart the service

```bash
ssh user@10.0.10.51 'sudo pkill -9 strixcamfake; sudo pkill -9 ffmpeg; sleep 1; cd ~/StrixCamFake && sudo nohup ./strixcamfake > /tmp/strixcamfake.log 2>&1 &'
```

### Step 4: Verify it's running

Wait 5 seconds for ffmpeg producers to connect, then run quick checks:

```bash
sleep 5
ssh user@10.0.10.51 'ps aux | grep strixcamfake | grep -v grep'
```

Then test at least one RTSP stream and one HTTP endpoint:

```bash
timeout 10 ffprobe -v error -rtsp_transport tcp -show_entries stream=codec_name,width,height -of csv=p=0 rtsp://admin:admin@10.0.10.51:554/Streaming/Channels/101
curl -s -o /dev/null -w "HTTP %{http_code}\n" http://10.0.10.51/
```

### Step 5: Report results

Tell the user whether deploy was successful and what was verified.

## Troubleshooting

- If `git pull` fails with conflicts: `ssh user@10.0.10.51 'cd ~/StrixCamFake && git checkout -- . && git pull'`
- If port already in use: the pkill in Step 3 handles this
- To check logs: `ssh user@10.0.10.51 'tail -30 /tmp/strixcamfake.log'`
