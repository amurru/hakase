import numpy as np
from PIL import Image
import os

def draw_mandelbrot_png(
    width=800,
    height=600,
    xlim=(-2.0, 1.0),
    ylim=(-1.2, 1.2),
    max_iter=100,
    output_path='./outputs/mandelbrot.png',
    colormap='viridis',
    escape_radius=2.0,
):
    """Draw the Mandelbrot set (often called 'Mandelbrot Groups' in colloquial usage)
    and save it to a PNG file using Pillow.

    Generates the famous Mandelbrot fractal by iterating the complex recurrence
    z -> z*z + c for every pixel and recording how quickly each value escapes.
    The resulting iteration counts are mapped to colors via a colormap.

    Parameters
    ----------
    width : int
        Image width in pixels.
    height : int
        Image height in pixels.
    xlim : tuple of float
        Real-axis range (left, right) of the complex plane to render.
    ylim : tuple of float
        Imaginary-axis range (bottom, top) of the complex plane to render.
    max_iter : int
        Maximum number of iterations per point. Higher = more detail.
    output_path : str
        File path where the PNG is saved.
    colormap : str
        Matplotlib colormap name to use for coloring (e.g. 'viridis', 'plasma',
        'magma', 'hot', 'twilight'). A built-in HSV spectrum is used as fallback
        if matplotlib is unavailable.
    escape_radius : float
        Radius that determines when a point has escaped the set.

    Returns
    -------
    str
        The output path of the saved PNG file.

    Usage:
        from skills.draw_mandelbrot_png import draw_mandelbrot_png
        draw_mandelbrot_png(
            width=1200, height=800,
            xlim=(-2.0, 1.0), ylim=(-1.2, 1.2),
            max_iter=200,
            output_path='./outputs/mandelbrot.png',
            colormap='twilight',
        )
    """
    os.makedirs(os.path.dirname(os.path.abspath(output_path)), exist_ok=True)

    # Build complex plane grid
    re = np.linspace(xlim[0], xlim[1], width, dtype=np.float64)
    im = np.linspace(ylim[0], ylim[1], height, dtype=np.float64)
    C = re[np.newaxis, :] + 1j * im[:, np.newaxis]

    # Escape-time algorithm
    M = np.full(C.shape, max_iter, dtype=np.int32)
    Z = np.zeros_like(C, dtype=np.complex128)
    active = np.full(C.shape, True, dtype=bool)

    for i in range(max_iter):
        Z[active] = Z[active] * Z[active] + C[active]
        escaped = np.abs(Z) > escape_radius
        newly = escaped & active
        M[newly] = i
        active = active & ~escaped

    norm = M.astype(np.float64) / max_iter

    def _get_cmap(name):
        """Retrieve a matplotlib colormap by name (handles both old and new APIs)."""
        try:
            import matplotlib.colormaps as cm
            if name in cm.cmap_d:
                return cm.cmap_d[name]
        except Exception:
            pass
        try:
            import matplotlib.cm as cm
            if hasattr(cm, 'get_cmap'):
                return cm.get_cmap(name)
        except Exception:
            pass
        return None

    rgb = None
    cmap = _get_cmap(colormap)
    if cmap is None:
        cmap = _get_cmap('viridis')
    if cmap is not None:
        rgba = cmap(norm)
        rgb = (rgba[:, :, :3] * 255).astype(np.uint8)

    if rgb is None:
        # HSV-to-RGB fallback based on normalized iteration count
        h = norm * 5.0
        s = np.ones_like(h)
        v = np.where(M < max_iter, 1.0, 0.0)
        i = np.floor(h).astype(int)
        f = h - i
        p = v * (1 - s)
        q = v * (1 - s * f)
        t = v * (1 - s * (1 - f))
        i0 = i % 6
        r = np.choose(i0, [v, q, p, p, t, v])
        g = np.choose(i0, [t, v, v, q, p, p])
        b = np.choose(i0, [p, p, t, v, v, q])
        rgb = np.stack([r, g, b], axis=-1)
        rgb = (np.clip(rgb, 0, 1) * 255).astype(np.uint8)

    img = Image.fromarray(rgb, mode='RGB')
    img.save(output_path, format='PNG')
    return output_path


if __name__ == "__main__":
    # Full Mandelbrot set view
    draw_mandelbrot_png(
        width=900,
        height=600,
        xlim=(-2.0, 1.0),
        ylim=(-1.2, 1.2),
        max_iter=150,
        output_path='./outputs/mandelbrot.png',
        colormap='twilight',
    )
    print('Full view saved to ./outputs/mandelbrot.png')

    # Zoomed region (a "sea horse valley" / mini-Mandelbrot area)
    draw_mandelbrot_png(
        width=900,
        height=900,
        xlim=(-0.74877, -0.74225),
        ylim=(0.09315, 0.14067),
        max_iter=300,
        output_path='./outputs/mandelbrot_zoom.png',
        colormap='magma',
    )
    print('Zoom view saved to ./outputs/mandelbrot_zoom.png')