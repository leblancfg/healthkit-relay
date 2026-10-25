"""Draws the docs' social preview images from the logo and the tagline.

    nix-shell -p 'python3.withPackages (p: [ p.pillow ])' -p inter \
      --run 'python3 scripts/social-image.py'

Writes docs/assets/social.png (1200x630, Open Graph and Twitter cards) and
docs/assets/social-github.png (1280x640, the GitHub repository's social preview).
"""

import glob
import os
import sys

from PIL import Image, ImageDraw, ImageFont

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CORAL = (255, 45, 85)
INK = (28, 28, 30)
GREY = (99, 99, 102)


def font(weight, size):
    paths = glob.glob("/nix/store/*-inter-*/share/fonts/truetype/InterVariable.ttf")
    paths += glob.glob("/usr/share/fonts/**/InterVariable.ttf", recursive=True)
    if not paths:
        sys.exit("Inter not found: run with -p inter")
    f = ImageFont.truetype(paths[0], size)
    f.set_variation_by_axes([14 if size < 32 else 32, weight])  # axes: optical size, weight
    return f


def draw(width, height, out):
    logo = Image.open(os.path.join(ROOT, "docs/assets/logo.png")).convert("RGB")
    im = Image.new("RGB", (width, height), logo.getpixel((0, 0)))
    d = ImageDraw.Draw(im)
    d.rectangle([0, height - 14, width, height], fill=CORAL)

    size = int(height * 0.52)
    logo = logo.resize((size, size), Image.LANCZOS)
    left = int(width * 0.06)
    # The logo's near-white background becomes transparent, so it carries no visible box.
    mask = logo.convert("L").point(lambda v: max(0, min(255, (250 - v) * 6)))
    im.paste(logo, (left, (height - size) // 2 - 10), mask)

    x = left + size + int(width * 0.03)
    y = int(height * 0.25)
    d.text((x, y), "Health Relay", font=font(800, 84), fill=INK)
    y += 112
    for line in ("Your Apple Health data,", "on a server you own."):
        d.text((x, y), line, font=font(600, 44), fill=CORAL)
        y += 56
    y += 22
    for line in ("Free, open-source iPhone app and self-hosted server.",
                 "Parquet archive, DuckDB, Grafana."):
        d.text((x, y), line, font=font(450, 28), fill=GREY)
        y += 38
    d.text((x, height - 70), "health-relay.leblancfg.com", font=font(600, 26), fill=INK)
    im.save(os.path.join(ROOT, out), optimize=True)


draw(1200, 630, "docs/assets/social.png")
draw(1280, 640, "docs/assets/social-github.png")
