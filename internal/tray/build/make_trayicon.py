"""Generate a monochrome macOS template icon for the Autoapi menu-bar tray.

Design: three filled black nodes arranged in an equilateral triangle,
connected by 2px black lines. Conceptually represents routing requests
across multiple upstream providers. Rendered at 8x supersampling for
clean anti-aliased edges.
"""
from PIL import Image, ImageDraw
from PIL.Image import Resampling

SIZE = 44
SCALE = 8  # supersampling factor
SS = SIZE * SCALE  # 352x352 work canvas

# Glyph parameters (in final 44x44 coordinates).
# Triangle vertices — inscribed roughly within a central 20x20 area.
TOP = (22.0, 12.0)
BL = (12.0, 32.0)
BR = (32.0, 32.0)

NODE_RADIUS = 3.5
STROKE = 2.0

FG = (0, 0, 0, 255)


def to_ss(x, y):
    """Scale final coords to supersampled coords."""
    return x * SCALE, y * SCALE


img = Image.new("RGBA", (SS, SS), (0, 0, 0, 0))
draw = ImageDraw.Draw(img)

# Connecting lines first so node circles sit on top and cap them cleanly.
for a, b in ((TOP, BL), (BL, BR), (BR, TOP)):
    draw.line(
        [to_ss(*a), to_ss(*b)],
        fill=FG,
        width=int(round(STROKE * SCALE)),
    )

# Nodes as filled circles.
for center in (TOP, BL, BR):
    cx, cy = to_ss(*center)
    r = NODE_RADIUS * SCALE
    draw.ellipse((cx - r, cy - r, cx + r, cy + r), fill=FG)

# Downsample with LANCZOS for anti-aliased alpha.
out = img.resize((SIZE, SIZE), Resampling.LANCZOS)

out.save("internal/tray/build/trayicon.png", "PNG")
print(f"Wrote internal/tray/build/trayicon.png ({SIZE}x{SIZE})")
