"""Validate public recording JSON and finalized media using existing test tools."""
import json
import os
import subprocess
import sys

status = json.load(sys.stdin)
assert not status["error"], status
assert status["droppedFrames"] == 0, status
if sys.argv[1] == "status":
    print(status["phase"])
    print(status["frames"])
else:
    path, width, height, codec = sys.argv[2:]
    codec = ["h264", "hevc", "av1"][int(codec)]
    assert status["phase"] == "completed" and status["frames"] >= 120, status
    assert status["codec"] == codec and status["path"] == path, status
    if os.environ.get("MYGOFRAME_EXPERIMENTAL_RECORDING_TRANSFER") == "vulkan":
        assert status["transfer"].startswith("vulkan-"), status
    report = json.loads(subprocess.check_output([
        "ffprobe", "-v", "error", "-select_streams", "v:0", "-count_packets",
        "-show_entries", "stream=codec_name,width,height,avg_frame_rate,nb_read_packets",
        "-of", "json", path,
    ], timeout=30))
    assert len(report["streams"]) == 1, report
    stream = report["streams"][0]
    assert (stream["codec_name"], stream["width"], stream["height"], stream["avg_frame_rate"]) == (codec, int(width), int(height), "60/1"), stream
    assert int(stream["nb_read_packets"]) == status["frames"], (stream, status)
    decoded = subprocess.run(["ffmpeg", "-v", "error", "-threads", "2", "-i", path, "-f", "null", "-"], capture_output=True, text=True, timeout=60)
    assert decoded.returncode == 0 and not decoded.stderr, decoded.stderr
    print(json.dumps({"path": path, "frames": status["frames"], "codec": codec,
                      "transfer": status["transfer"], "decode": "passed"}))
