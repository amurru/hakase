import numpy as np
import matplotlib.pyplot as plt
from matplotlib.colors import LinearSegmentedColormap
from matplotlib.collections import LineCollection
from PIL import Image
import os

def clifford_attractor(n_points=200000, a=1.5, b=-1.8, c=1.4, d=0.7, x0=0.1, y0=0.1):
    """
    Generate points for a Clifford attractor.
    
    Parameters:
    -----------
    n_points : int
        Number of iterations to generate.
    a, b, c, d : float
        Parameters controlling the attractor shape.
    x0, y0 : float
        Initial starting coordinates.
    
    Returns:
    --------
    x, y : np.ndarray
        Arrays of x and y coordinates of the attractor points.
    """
    x = np.zeros(n_points)
    y = np.zeros(n_points)
    x[0] = x0
    y[0] = y0
    for i in range(1, n_points):
        x[i] = np.sin(a * y[i-1]) + c * np.cos(a * x[i-1])
        y[i] = np.sin(b * x[i-1]) + d * np.cos(b * y[i-1])
    return x, y

def de_jong_attractor(n_points=200000, a=1.4, b=-2.3, c=-0.8, d=1.7, x0=0.1, y0=0.1):
    """
    Generate points for a Peter De Jong attractor.
    
    Parameters:
    -----------
    n_points : int
        Number of iterations to generate.
    a, b, c, d : float
        Parameters controlling the attractor shape.
    x0, y0 : float
        Initial starting coordinates.
    
    Returns:
    --------
    x, y : np.ndarray
        Arrays of x and y coordinates of the attractor points.
    """
    x = np.zeros(n_points)
    y = np.zeros(n_points)
    x[0] = x0
    y[0] = y0
    for i in range(1, n_points):
        x[i] = np.sin(a * y[i-1]) - np.cos(b * x[i-1])
        y[i] = np.sin(c * x[i-1]) - np.cos(d * y[i-1])
    return x, y

def ikeda_attractor(n_points=200000, a=0.4, b=0.1, c=1.0, d=1.0, x0=0.1, y0=0.1):
    """
    Generate points for an Ikeda attractor.
    
    Parameters:
    -----------
    n_points : int
        Number of iterations to generate.
    a, b, c, d : float
        Parameters controlling the attractor shape.
    x0, y0 : float
        Initial starting coordinates.
    
    Returns:
    --------
    x, y : np.ndarray
        Arrays of x and y coordinates of the attractor points.
    """
    x = np.zeros(n_points)
    y = np.zeros(n_points)
    x[0] = x0
    y[0] = y0
    for i in range(1, n_points):
        t = 1.0 + x[i-1] * x[i-1] + y[i-1] * y[i-1]
        x[i] = 1.0 + a * (x[i-1] * np.cos(t) + y[i-1] * np.sin(t))
        y[i] = b * (x[i-1] * np.sin(t) - y[i-1] * np.cos(t))
    return x, y

def morphing_clifford_attractor(n_points=500000, n_cycles=3, x0=0.1, y0=0.1):
    """
    Generate a morphing Clifford attractor where parameters change over iterations.
    
    Parameters:
    -----------
    n_points : int
        Number of iterations to generate.
    n_cycles : float
        Number of parameter morphing cycles through the generation.
    x0, y0 : float
        Initial starting coordinates.
    
    Returns:
    --------
    x, y : np.ndarray
        Arrays of x and y coordinates of the morphing attractor points.
    """
    x = np.zeros(n_points)
    y = np.zeros(n_points)
    x[0] = x0
    y[0] = y0
    
    for i in range(1, n_points):
        t = i / n_points
        a = 1.5 + 0.5 * np.sin(t * 2 * np.pi * n_cycles)
        b = -1.8 + 0.3 * np.cos(t * 2 * np.pi * n_cycles * 1.7)
        c = 1.4 + 0.4 * np.sin(t * 2 * np.pi * n_cycles * 0.7)
        d = 0.7 + 0.3 * np.cos(t * 2 * np.pi * n_cycles * 1.3)
        
        x[i] = np.sin(a * y[i-1]) + c * np.cos(a * x[i-1])
        y[i] = np.sin(b * x[i-1]) + d * np.cos(b * y[i-1])
    return x, y

def create_strange_attractor_composite(output_path='./outputs/strange_attractors.png',
                                     n_points=300000,
                                     clifford_params=(1.4, -1.7, 1.3, 0.8),
                                     dejong_params=(1.3, -2.1, -0.9, 1.5),
                                     ikeda_params=(0.5, 0.2, 1.2, 0.8)):
    """
    Create a composite visualization of multiple strange attractors.
    
    Parameters:
    -----------
    output_path : str
        Path to save the output PNG file.
    n_points : int
        Number of points per attractor.
    clifford_params : tuple of float
        (a, b, c, d) parameters for Clifford attractor.
    dejong_params : tuple of float
        (a, b, c, d) parameters for De Jong attractor.
    ikeda_params : tuple of float
        (a, b, c, d) parameters for Ikeda attractor.
    
    Returns:
    --------
    str
        Path to the saved image file.
    
    Usage:
    ------
    from skills.create_strange_attractors import create_strange_attractor_composite
    create_strange_attractor_composite('./outputs/my_strange_plot.png', n_points=500000)
    """
    os.makedirs(os.path.dirname(output_path), exist_ok=True)
    
    a1, b1, c1, d1 = clifford_params
    a2, b2, c2, d2 = dejong_params
    a3, b3, c3, d3 = ikeda_params
    
    x1, y1 = clifford_attractor(n_points, a=a1, b=b1, c=c1, d=d1)
    x2, y2 = de_jong_attractor(n_points, a=a2, b=b2, c=c2, d=d2)
    x3, y3 = ikeda_attractor(n_points, a=a3, b=b3, c=c3, d=d3)
    
    fig = plt.figure(figsize=(16, 12), facecolor='black')
    ax = fig.add_subplot(111, facecolor='black')
    
    ax.hexbin(x1, y1, gridsize=1200, cmap='Purples', mincnt=1, alpha=0.6)
    ax.hexbin(x2, y2, gridsize=1200, cmap='Greens', mincnt=1, alpha=0.5)
    ax.hexbin(x3, y3, gridsize=1200, cmap='Reds', mincnt=1, alpha=0.4)
    
    ax.scatter(x1[::500], y1[::500], s=0.1, c='cyan', alpha=0.3)
    ax.scatter(x2[::500], y2[::500], s=0.1, c='magenta', alpha=0.3)
    ax.scatter(x3[::500], y3[::500], s=0.1, c='yellow', alpha=0.3)
    
    ax.set_xlim(-3, 3)
    ax.set_ylim(-3, 3)
    ax.set_aspect('equal')
    ax.set_facecolor('black')
    
    ax.set_xticks([])
    ax.set_yticks([])
    for spine in ax.spines.values():
        spine.set_visible(False)
    
    plt.title('STRANGE ATTRACTOR CHRONICLES\nThree Chaotic Systems Colliding', 
              fontsize=16, color='white', pad=20, fontfamily='monospace')
    
    plt.tight_layout()
    plt.savefig(output_path, dpi=300, facecolor='black', bbox_inches='tight', 
                pad_inches=0.1, edgecolor='none')
    plt.close()
    
    return output_path

def create_morphing_attractor(output_path='./outputs/morphing_attractor.png',
                            n_points=500000, n_cycles=5, x0=0.1, y0=0.1):
    """
    Create a morphing Clifford attractor visualization with gradient coloring.
    
    Parameters:
    -----------
    output_path : str
        Path to save the output PNG file.
    n_points : int
        Number of iterations to generate.
    n_cycles : float
        Number of parameter morphing cycles.
    x0, y0 : float
        Initial starting coordinates.
    
    Returns:
    --------
    str
        Path to the saved image file.
    
    Usage:
    ------
    from skills.create_strange_attractors import create_morphing_attractor
    create_morphing_attractor('./outputs/morph.png', n_points=1000000)
    """
    os.makedirs(os.path.dirname(output_path), exist_ok=True)
    
    x, y = morphing_clifford_attractor(n_points, n_cycles, x0, y0)
    
    fig = plt.figure(figsize=(14, 14), facecolor='black')
    ax = fig.add_subplot(111, facecolor='black')
    
    points = np.array([x, y]).T.reshape(-1, 1, 2)
    segments = np.concatenate([points[:-1], points[1:]], axis=1)
    
    norm = plt.Normalize(0, len(x))
    lc = LineCollection(segments[:, :, :], cmap='plasma', norm=norm, alpha=0.7, linewidth=0.3)
    lc.set_array(np.arange(len(x)))
    ax.add_collection(lc)
    
    ax.scatter(x[::50], y[::50], c=np.arange(len(x[::50])), 
               cmap='twilight', s=0.2, alpha=0.6)
    
    ax.set_xlim(-3, 3)
    ax.set_ylim(-3, 3)
    ax.set_aspect('equal')
    
    ax.set_xticks([])
    ax.set_yticks([])
    for spine in ax.spines.values():
        spine.set_visible(False)
    
    plt.title('THE MORPHING DRAGON\nA Shape-Shifting Strange Attractor', 
              fontsize=18, color='white', pad=20, fontfamily='monospace')
    
    plt.tight_layout()
    plt.savefig(output_path, dpi=300, facecolor='black', bbox_inches='tight', pad_inches=0.1)
    plt.close()
    
    return output_path

if __name__ == "__main__":
    print("Generating strange attractor visualizations...")
    
    # Create composite attractor
    path1 = create_strange_attractor_composite(
        output_path='./outputs/strange_attractors.png',
        n_points=300000
    )
    print(f"Composite attractor saved to: {path1}")
    
    # Create morphing attractor
    path2 = create_morphing_attractor(
        output_path='./outputs/morphing_attractor.png',
        n_points=500000
    )
    print(f"Morphing attractor saved to: {path2}")
    
    # Verify files
    for p in [path1, path2]:
        if os.path.exists(p):
            img = Image.open(p)
            size_kb = os.path.getsize(p) / 1024
            print(f"  Verified {p}: {img.size}, {size_kb:.1f} KB")
