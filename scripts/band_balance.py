#!/usr/bin/env python3
"""書き出した音の帯域ごとのバランスを、基準(普通は原音)と比べる。

使い方: python3 scripts/band_balance.py 基準.wav 比較1.wav 比較2.wav ...

各ファイルの 1/3オクターブ帯域の長時間平均レベル(dB)を、全帯域の合計エネルギーをそろえたうえで、
基準との差として表示する。あわせて、帯域ごとの S/M(左右差成分 ÷ 中央成分、dB。大きいほど左右に広がる)。
ffmpeg で読むので、WAV 以外の音源もそのまま渡せる。numpy が必要(音作りの検証用で、ビルドには使わない)。
"""
import subprocess
import sys

import numpy as np


def read(path):
    raw = subprocess.run(["ffmpeg", "-v", "error", "-i", path, "-f", "f32le", "-ac", "2", "-ar", "48000", "-"],
                         check=True, capture_output=True).stdout
    return 48000, np.frombuffer(raw, "<f4").astype(np.float64).reshape(-1, 2)


def spectra(x, sr, nfft=16384):
    hop = nfft // 2
    win = np.hanning(nfft)
    m = (x[:, 0] + x[:, 1]) / 2
    s = (x[:, 0] - x[:, 1]) / 2
    pm = np.zeros(nfft // 2 + 1)
    ps = np.zeros(nfft // 2 + 1)
    cnt = 0
    for i in range(0, len(m) - nfft, hop):
        pm += np.abs(np.fft.rfft(m[i:i + nfft] * win)) ** 2
        ps += np.abs(np.fft.rfft(s[i:i + nfft] * win)) ** 2
        cnt += 1
    f = np.fft.rfftfreq(nfft, 1 / sr)
    return f, pm / cnt, ps / cnt


centers = [25, 31.5, 40, 50, 63, 80, 100, 125, 160, 200, 250, 315, 400, 500, 630, 800, 1000, 1250, 1600,
           2000, 2500, 3150, 4000, 5000, 6300, 8000, 10000, 12500, 16000]


def bands(f, p):
    out = []
    for c in centers:
        lo, hi = c / 2 ** (1 / 6), c * 2 ** (1 / 6)
        sel = (f >= lo) & (f < hi)
        out.append(p[sel].sum())
    return np.array(out)


files = sys.argv[1:]
res = []
for path in files:
    sr, x = read(path)
    f, pm, ps = spectra(x, sr)
    bm, bs = bands(f, pm), bands(f, ps)
    tot = (bm + bs).sum()
    level = 10 * np.log10((bm + bs) / tot)
    sm = 10 * np.log10(bs / bm)
    res.append((path, level, sm))

names = [r[0].rsplit("/", 1)[-1].removesuffix(".wav") for r in res]
print("帯域ごとのレベル(全体のエネルギーをそろえ、基準=%s との差 dB)" % names[0])
print("   Hz  " + "".join(f"{n[:12]:>13}" for n in names[1:]))
for i, c in enumerate(centers):
    print(f"{c:6g} " + "".join(f"{r[1][i] - res[0][1][i]:13.1f}" for r in res[1:]))
print()
print("帯域ごとの S/M(dB。左右差成分 ÷ 中央成分)")
print("   Hz  " + "".join(f"{n[:12]:>13}" for n in names))
for i, c in enumerate(centers):
    print(f"{c:6g} " + "".join(f"{r[2][i]:13.1f}" for r in res))
