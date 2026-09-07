"""
PDF Report Generator using WeasyPrint

A reusable skill for generating professional, data-rich PDF reports from
Markdown content with embedded charts and styled tables. This module provides
functions to create charts, compile research data into Markdown, and convert
to PDF format suitable for business reports, research summaries, and analysis documents.

Usage:
    from pdf_report_generator import create_chart, compile_report, md_to_pdf
    
    # Create a bar chart
    create_chart(
        chart_type='bar',
        data={'Method A': 50, 'Method B': 75, 'Method C': 30},
        title='Comparison Chart',
        output_path='./outputs/chart.png'
    )
    
    # Compile full report from structured data
    md = compile_report(
        title='My Report',
        sections=[{'heading': 'Section 1', 'content': '...'}],
        output_md='./outputs/report.md',
        output_pdf='./outputs/report.pdf'
    )
    
    # Convert existing markdown to PDF
    md_to_pdf('./outputs/report.md', './outputs/report.pdf')

Author: code_interpreter
Date: 2026
"""

import os
import markdown
from weasyprint import HTML, CSS
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt


def create_chart(chart_type='bar', data=None, title='Chart', x_label='X', y_label='Y',
                 output_path='./outputs/chart.png', figsize=(10, 6),
                 colors=None, dpi=150, show_values=True):
    """
    Creates a chart image (bar, line, or pie) and saves it to a PNG file.

    Parameters:
        chart_type (str): 'bar', 'line', or 'pie'. Default: 'bar'.
        data (dict): Dictionary of label -> value pairs. Default: None.
        title (str): Chart title. Default: 'Chart'.
        x_label (str): X-axis label. Default: 'X'.
        y_label (str): Y-axis label. Default: 'Y'.
        output_path (str): Path to save the chart PNG. Default: './outputs/chart.png'.
        figsize (tuple): Figure size in inches. Default: (10, 6).
        colors (list): List of color hex codes. Default: None (auto).
        dpi (int): Resolution. Default: 150.
        show_values (bool): Show values on charts. Default: True.

    Returns:
        str: Path to the saved chart file.

    Usage:
        create_chart(
            chart_type='bar',
            data={'A': 10, 'B': 20, 'C': 15},
            title='Sales by Quarter',
            output_path='./outputs/sales_chart.png'
        )
    """
    os.makedirs(os.path.dirname(output_path) or '.', exist_ok=True)
    
    if data is None:
        data = {'A': 10, 'B': 20, 'C': 15}
    
    labels = list(data.keys())
    values = list(data.values())
    
    if colors is None:
        default_colors = ['#2E86AB', '#A23B72', '#F18F01', '#C73E1D', '#3B1F5E', '#8AC926', '#198244', '#6B423D']
        colors = default_colors * ((len(labels) // len(default_colors)) + 1)
    
    fig, ax = plt.subplots(figsize=figsize)
    
    if chart_type == 'bar':
        bars = ax.barh(labels, values, color=colors[:len(labels)], edgecolor='white', linewidth=1.5)
        ax.set_xlabel(x_label)
        ax.set_title(title)
        if show_values:
            for bar, val in zip(bars, values):
                ax.text(bar.get_width() + 0.5, bar.get_y() + bar.get_height()/2, str(val),
                        va='center', fontsize=10, fontweight='bold')
    elif chart_type == 'line':
        ax.plot(labels, values, marker='o', color='#2E86AB', linewidth=2, markersize=8)
        ax.set_xlabel(x_label)
        ax.set_ylabel(y_label)
        ax.set_title(title)
        plt.xticks(rotation=45, ha='right')
    elif chart_type == 'pie':
        ax.pie(values, labels=labels, autopct='%1.0f%%', startangle=90,
               colors=colors[:len(labels)], textprops={'fontsize': 10, 'fontweight': 'bold'},
               wedgeprops={'edgecolor': 'white', 'linewidth': 2})
        ax.set_title(title)
    
    ax.set_facecolor('#F8F9FA')
    fig.patch.set_facecolor('#F8F9FA')
    
    plt.tight_layout()
    plt.savefig(output_path, dpi=dpi, bbox_inches='tight', facecolor='#F8F9FA', edgecolor='none')
    plt.close()
    
    return output_path


def md_to_pdf(input_path, output_path):
    """
    Converts a Markdown file to a styled PDF using WeasyPrint.

    Parameters:
        input_path (str): Path to the input Markdown file.
        output_path (str): Path for the output PDF file.

    Returns:
        str: Path to the generated PDF file.

    Usage:
        md_to_pdf('./outputs/report.md', './outputs/report.pdf')
    """
    with open(input_path, 'r', encoding='utf-8') as f:
        md_text = f.read()
    
    html_text = markdown.markdown(md_text, extensions=['tables', 'fenced_code', 'toc', 'extra', 'codehilite'])
    
    css = '''
    @page { size: A4; margin: 0.8in; }
    @font-face { font-family: 'DejaVu Sans'; font-style: normal; }
    body { font-family: 'DejaVu Sans', Arial, sans-serif; line-height: 1.6; color: #333; max-width: 900px; margin: 0 auto; padding: 10px; background: white; }
    h1 { color: #0c2948; font-size: 28px; border-bottom: 3px solid #2E86AB; padding-bottom: 10px; }
    h2 { color: #1a365d; font-size: 22px; border-bottom: 1px solid #ddd; padding-bottom: 5px; margin-top: 1.8em; }
    h3 { color: #2c5282; font-size: 18px; }
    h4 { color: #2d3748; font-size: 16px; }
    table { border-collapse: collapse; width: 100%; margin: 0.8em 0; font-size: 13px; }
    th, td { border: 1px solid #ddd; padding: 6px 10px; text-align: left; }
    th { background-color: #2E86AB; color: white; font-weight: bold; }
    tr:nth-child(even) { background-color: #f8f9fa; }
    code { background-color: #f1f1f1; padding: 2px 5px; border-radius: 3px; font-family: 'Courier New', monospace; font-size: 12px; }
    blockquote { border-left: 4px solid #2E86AB; padding-left: 15px; margin: 0.8em 0; color: #555; font-style: italic; background-color: #f8f9fa; padding-top: 5px; }
    img { max-width: 100%; height: auto; margin: 10px 0; }
    .report-header { text-align: center; margin-bottom: 1em; padding-bottom: 10px; }
    .report-footer { text-align: center; margin-top: 2em; padding-top: 1em; border-top: 1px solid #ddd; color: #777; font-size: 11px; }
    '''
    
    html_template = f'''<!DOCTYPE html><html><head><meta charset="utf-8"><title>Report</title><style>{css}</style></head><body>{html_text}</body></html>'''
    
    HTML(string=html_template, base_url=os.path.dirname(input_path) or '.').write_pdf(output_path, stylesheets=[CSS(string=css)])
    
    return output_path


def compile_report(title, sections, output_md='./outputs/report.md', output_pdf=None,
                    footer_text=None):
    """
    Compiles a structured report from sections into Markdown and optionally converts to PDF.

    Parameters:
        title (str): Report title.
        sections (list): List of dicts with 'heading' and 'content' keys.
            Optionally 'level' (default 2) and 'image' (path).
        output_md (str): Path for output Markdown file.
        output_pdf (str): Path for output PDF file (if None, skips PDF).
        footer_text (str): Footer text for PDF report.

    Returns:
        str: Markdown content of the report.

    Usage:
        md = compile_report(
            title='My Report',
            sections=[
                {'heading': 'Introduction', 'content': 'This report covers...'},
                {'heading': 'Findings', 'content': '**Key finding**: ...', 'level': 3}
            ],
            output_md='./outputs/report.md',
            output_pdf='./outputs/report.pdf'
        )
    """
    # Canonicalize the output location and keep it inside the current
    # directory tree: absolute paths must pass the same containment check as
    # relative ones (including traversal segments), so a caller-supplied path
    # can never write elsewhere.
    cwd = os.path.realpath(os.getcwd())
    output_md = os.path.realpath(os.path.join(cwd, output_md))
    if os.path.commonpath([cwd, output_md]) != cwd:
        raise ValueError("output_md must stay inside the current directory")
    os.makedirs(os.path.dirname(output_md) or '.', exist_ok=True)
    
    md_parts = [f"# {title}\n\n"]
    
    for section in sections:
        level = section.get('level', 2)
        heading_prefix = '#' * level
        md_parts.append(f"{heading_prefix} {section['heading']}\n\n")
        
        if section.get('image'):
            md_parts.append(f"![{section.get('image_alt', section['heading'])}]({section['image']})\n\n")
        
        md_parts.append(f"{section['content']}\n\n")
        
        if section.get('table'):
            table = section['table']
            md_parts.append('| ' + ' | '.join(table['headers']) + ' |\n')
            md_parts.append('| ' + ' | '.join(['---'] * len(table['headers'])) + ' |\n')
            for row in table['rows']:
                md_parts.append('| ' + ' | '.join(str(c) for c in row) + ' |\n')
            md_parts.append('\n')
    
    if footer_text:
        md_parts.append(f"\n---\n\n{footer_text}")
    
    md_content = ''.join(md_parts)
    
    with open(output_md, 'w', encoding='utf-8') as f:
        f.write(md_content)
    
    if output_pdf:
        md_to_pdf(output_md, output_pdf)
        print(f"PDF saved: {output_pdf}")
    
    return md_content


if __name__ == "__main__":
    # Path-confinement self-test: an absolute output path outside the current
    # directory must be rejected before any file is created. The test only
    # ever touches its own freshly created temporary directory (never a fixed
    # name), and cleans that directory up in finally - so it cannot delete a
    # pre-existing file even when compile_report rejects the path.
    import shutil
    import tempfile
    tmpdir = tempfile.mkdtemp(prefix="hakase_pdf_selftest_")
    try:
        outside_md = os.path.join(tmpdir, "outside.md")
        try:
            compile_report("x", [{'heading': 'h', 'content': 'c'}], output_md=outside_md)
        except ValueError:
            print("✅ Path confinement: absolute path outside cwd rejected")
        else:
            raise SystemExit("path confinement failed: absolute output outside cwd was accepted")
    finally:
        shutil.rmtree(tmpdir, ignore_errors=True)

    # Sample mock data for testing
    print("Testing PDF Report Generator\n")
    
    # Test chart creation
    create_chart(
        chart_type='bar',
        data={'Gig Apps': 25, 'AI Services': 54, 'Content': 44, 'Freelance': 40},
        title='Sample Earnings Comparison',
        x_label='Hourly Rate ($)',
        output_path='./outputs/test_chart.png'
    )
    print("✅ Test chart created: ./outputs/test_chart.png")
    
    # Test report compilation
    sections = [
        {'heading': 'Introduction', 'content': 'This is a test report demonstrating the PDF report generator capability.'},
        {'heading': 'Key Methods', 'content': '**Method A**: Description here.\n\n**Method B**: Description here.'},
        {'heading': 'Data Table', 'content': 'Here is a summary table:',
         'table': {'headers': ['Method', 'Rate', 'Time'], 'rows': [['Gig Apps', '$25/hr', 'Minutes'], ['AI Work', '$54/hr', 'Hours']]}},
    ]
    
    md = compile_report(
        title='Test Report',
        sections=sections,
        output_md='./outputs/test_report.md',
        output_pdf='./outputs/test_report.pdf'
    )
    print("✅ Test report created: ./outputs/test_report.pdf")
    print(f"\nReport length: {len(md)} characters")
