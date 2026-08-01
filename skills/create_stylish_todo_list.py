from reportlab.lib import colors
from reportlab.lib.pagesizes import letter, A4
from reportlab.lib.styles import getSampleStyleSheet, ParagraphStyle
from reportlab.lib.units import inch, cm, mm
from reportlab.lib.enums import TA_LEFT, TA_CENTER, TA_RIGHT
from reportlab.platypus import SimpleDocTemplate, Paragraph, Spacer, Table, TableStyle, PageBreak
from reportlab.pdfbase import pdfmetrics
import os
from datetime import datetime


def create_stylish_todo_list(output_path='./outputs/stylish_todo_list.pdf',
                             page_size=A4,
                             primary_color='#4A6FA5',
                             accent_color='#D4A59A',
                             include_daily=True,
                             include_weekly=True,
                             include_general=True,
                             include_habits=True,
                             include_goals=True):
    """Create a stylish, printable to-do list PDF template.

    This function generates a beautiful, multi-page to-do list PDF with
    elegant minimalist design featuring soft color accents, clean typography,
    and organized sections for task management. The template is inspired by
    modern minimalist and bullet journal design trends found online.

    Pages included (configurable):
    - Cover page with title and design feature overview
    - Daily Tasks: Priority (H/M/L) task table with time, estimate, and done columns
    - Weekly Planning: 7-day overview table with priority matrix (Eisenhower Box)
    - General Tasks: Detailed project task tracker with checkboxes, categories, status
    - Progress Tracking: Weekly goal tracking table
    - Goal Setting & Reflection: Monthly goals, weekly reflection prompts
    - Habit Tracker: Monthly habit calendar grid (10 pre-defined healthy habits)
    - Monthly Overview: Year-long habit tracking summary

    Args:
        output_path (str): Path where the PDF will be saved.
            Defaults to './outputs/stylish_todo_list.pdf'
        page_size (tuple): ReportLab page size tuple (width, height).
            Defaults to A4 (used for standard printing).
        primary_color (str): Hex color for headers and accent elements.
            Defaults to '#4A6FA5' (soft blue).
        accent_color (str): Hex color for section titles and highlights.
            Defaults to '#D4A59A' (soft terracotta).
        include_daily (bool): Whether to include the Daily Tasks page.
            Defaults to True.
        include_weekly (bool): Whether to include the Weekly Planning page.
            Defaults to True.
        include_general (bool): Whether to include the General Tasks page.
            Defaults to True.
        include_habits (bool): Whether to include the Habit Tracker page.
            Defaults to True.
        include_goals (bool): Whether to include the Goal Setting page.
            Defaults to True.

    Returns:
        str: The path to the generated PDF file.

    Usage:
        >>> from skills.stylish_todo_list import create_stylish_todo_list
        >>> path = create_stylish_todo_list('./outputs/my_todo.pdf')
        >>> print(f"PDF created at: {path}")

        >>> # Create with custom colors and specific sections only
        >>> create_stylish_todo_list(
        ...     output_path='./outputs/daily_only.pdf',
        ...     primary_color='#2E86AB',
        ...     accent_color='#A23B72',
        ...     include_weekly=False,
        ...     include_habits=False
        ... )
    """

    # Color palette derived from parameters
    colors_palette = {
        'primary': colors.HexColor(primary_color),
        'secondary': colors.HexColor('#F2E8CF'),
        'accent': colors.HexColor(accent_color),
        'dark': colors.HexColor('#333333'),
        'light': colors.HexColor('#FFFFFF'),
        'border': colors.HexColor('#E8E1D3'),
        'light_gray': colors.HexColor('#F8F6F3'),
    }

    # Ensure output directory exists
    os.makedirs(os.path.dirname(output_path), exist_ok=True)

    # Create the PDF
    doc = SimpleDocTemplate(
        output_path,
        pagesize=page_size,
        topMargin=25,
        bottomMargin=25,
        leftMargin=25,
        rightMargin=25,
        title='Stylish To-Do List'
    )

    FONT_BOLD = 'Helvetica-Bold'
    FONT_NORMAL = 'Helvetica'

    styles = getSampleStyleSheet()

    # Custom styles
    title_style = ParagraphStyle(
        'CustomTitle',
        parent=styles['Heading1'],
        fontName=FONT_BOLD,
        fontSize=32,
        spaceAfter=10,
        textColor=colors_palette['primary'],
        alignment=TA_CENTER,
        leading=36,
    )

    subtitle_style = ParagraphStyle(
        'CustomSubtitle',
        parent=styles['Normal'],
        fontName=FONT_NORMAL,
        fontSize=16,
        spaceAfter=25,
        textColor=colors_palette['dark'],
        alignment=TA_CENTER,
        leading=22,
    )

    header_style = ParagraphStyle(
        'CustomHeader',
        parent=styles['Normal'],
        fontName=FONT_BOLD,
        fontSize=20,
        spaceAfter=12,
        textColor=colors_palette['primary'],
        alignment=TA_LEFT,
        leading=24,
    )

    section_title_style = ParagraphStyle(
        'SectionTitle',
        parent=styles['Normal'],
        fontName=FONT_BOLD,
        fontSize=13,
        spaceAfter=6,
        textColor=colors_palette['accent'],
        alignment=TA_LEFT,
        leading=16,
    )

    normal_style = ParagraphStyle(
        'CustomNormal',
        parent=styles['Normal'],
        fontName=FONT_NORMAL,
        fontSize=11,
        spaceAfter=6,
        textColor=colors_palette['dark'],
        alignment=TA_LEFT,
        leading=16,
    )

    small_style = ParagraphStyle(
        'Small',
        parent=styles['Normal'],
        fontName=FONT_NORMAL,
        fontSize=9,
        spaceAfter=4,
        textColor=colors_palette['dark'],
        alignment=TA_LEFT,
        leading=13,
    )

    story = []

    # ===== COVER PAGE =====
    story.append(Spacer(1, 90))
    story.append(Paragraph('STYLISH TO-DO LIST', title_style))
    story.append(Paragraph('Your Personal Planning Companion', subtitle_style))
    story.append(Spacer(1, 40))

    # Design features section
    features_data = [
        ['📅 Daily Tasks', '📋 Weekly Planning', '📝 General Tasks', '💡 Notes'],
        ['Priority Matrix', 'Progress Tracking', 'Goal Setting', 'Habit Tracker']
    ]
    features_table = Table(features_data, colWidths=[4.2*cm, 4.2*cm, 4.2*cm, 4.2*cm])
    features_table.setStyle(TableStyle([
        ('FONTNAME', (0, 0), (-1, -1), FONT_NORMAL),
        ('FONTSIZE', (0, 0), (-1, -1), 12),
        ('TEXTCOLOR', (0, 0), (-1, -1), colors_palette['dark']),
        ('ALIGN', (0, 0), (-1, -1), 'CENTER'),
        ('VALIGN', (0, 0), (-1, -1), 'MIDDLE'),
        ('BOTTOMPADDING', (0, 0), (-1, -1), 12),
        ('TOPPADDING', (0, 0), (-1, -1), 12),
    ]))
    story.append(features_table)
    story.append(Spacer(1, 30))

    story.append(Paragraph('Date: _________________________', normal_style))
    story.append(Paragraph('Name: _________________________', normal_style))
    story.append(Paragraph('Week of: _______________________', normal_style))
    story.append(Spacer(1, 30))

    story.append(Paragraph('<font size="9" color="#AAAAAA">Designed with ✨ for productive minds</font>',
                           ParagraphStyle('Footer', parent=styles['Normal'], alignment=TA_CENTER, spaceAfter=0)))
    story.append(Paragraph('', normal_style))
    story.append(Paragraph('✓ Printable & Stylish  •  ✓ Clean Minimalist Design  •  ✓ Professional Layout',
                           ParagraphStyle('Features', parent=styles['Normal'], alignment=TA_CENTER, spaceAfter=0, textColor=colors_palette['light_gray'], fontSize=10)))
    story.append(Paragraph('', normal_style))
    story.append(Paragraph('Made with ❤️ on ' + datetime.now().strftime('%Y'),
                           ParagraphStyle('Year', parent=styles['Normal'], alignment=TA_CENTER, spaceAfter=0, textColor=colors.HexColor('#CCCCCC'), fontSize=10)))
    story.append(Paragraph('', normal_style))
    story.append(Paragraph('Page 2+',
                           ParagraphStyle('PageNum', parent=styles['Normal'], alignment=TA_CENTER, spaceAfter=0, textColor=colors.HexColor('#CCCCCC'), fontSize=8)))
    story.append(Paragraph('', normal_style))

    story.append(PageBreak())

    # ===== DAILY TASKS PAGE =====
    if include_daily:
        story.append(Paragraph('📅 DAILY TASKS', header_style))
        story.append(Paragraph('Date: ________________  ', normal_style))
        story.append(Paragraph('Day: ________________  ', normal_style))
        story.append(Paragraph('Energy Level: ☐ High ☐ Medium ☐ Low', normal_style))
        story.append(Spacer(1, 10))

        daily_data = [
            ['Priority', 'Task', 'Time', 'Est.', 'Done'],
        ] + [['🅷️', '', '', '', '']] * 16
        # Alternate H, M, L priority rows
        for i in range(16):
            pri = ['🅷️', '🅼️', '🅻️'][i % 3]
            daily_data[i + 1] = [pri, '', '', '', '']

        daily_table = Table(daily_data, colWidths=[1.2*cm, 9.5*cm, 2.2*cm, 1.3*cm, 1.3*cm])
        daily_table.setStyle(TableStyle([
            ('BACKGROUND', (0, 0), (-1, 0), colors_palette['primary']),
            ('TEXTCOLOR', (0, 0), (-1, 0), colors_palette['light']),
            ('FONTNAME', (0, 0), (-1, 0), FONT_BOLD),
            ('FONTSIZE', (0, 0), (-1, 0), 12),
            ('ALIGN', (0, 0), (-1, 0), 'CENTER'),
            ('VALIGN', (0, 0), (-1, 0), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 0), (-1, 0), 10),
            ('TOPPADDING', (0, 0), (-1, 0), 10),
            ('FONTNAME', (0, 1), (-1, -1), FONT_NORMAL),
            ('FONTSIZE', (0, 1), (-1, -1), 10),
            ('TEXTCOLOR', (0, 1), (-1, -1), colors_palette['dark']),
            ('ALIGN', (3, 1), (4, -1), 'CENTER'),
            ('VALIGN', (0, 1), (-1, -1), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 1), (-1, -1), 8),
            ('TOPPADDING', (0, 1), (-1, -1), 8),
            ('GRID', (0, 0), (-1, -1), 0.5, colors_palette['border']),
            ('ROWBACKGROUNDS', (0, 1), (-1, -1), [colors_palette['light'], colors_palette['light_gray']]),
        ]))
        story.append(daily_table)
        story.append(Spacer(1, 15))

        # Notes section
        story.append(Paragraph('📝 Notes & Reflections:', section_title_style))
        story.append(Paragraph('Key Accomplishments:', normal_style))
        notes_data = [[''] for _ in range(3)]
        notes_table = Table(notes_data, colWidths=[15*cm])
        notes_table.setStyle(TableStyle([
            ('FONTNAME', (0, 0), (-1, -1), FONT_NORMAL),
            ('FONTSIZE', (0, 0), (-1, -1), 10),
            ('VALIGN', (0, 0), (-1, -1), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 0), (-1, -1), 22),
            ('TOPADDING', (0, 0), (-1, -1), 5),
            ('GRID', (0, 0), (-1, -1), 0.5, colors_palette['border']),
            ('ROWBACKGROUNDS', (0, 0), (-1, -1), [colors_palette['light'], colors_palette['light_gray']]),
        ]))
        story.append(notes_table)

        story.append(Spacer(1, 5))
        story.append(Paragraph('Lessons Learned:', normal_style))
        lessons_data = [[''] for _ in range(3)]
        lessons_table = Table(lessons_data, colWidths=[15*cm])
        lessons_table.setStyle(TableStyle([
            ('FONTNAME', (0, 0), (-1, -1), FONT_NORMAL),
            ('FONTSIZE', (0, 0), (-1, -1), 10),
            ('VALIGN', (0, 0), (-1, -1), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 0), (-1, -1), 22),
            ('TOPADDING', (0, 0), (-1, -1), 5),
            ('GRID', (0, 0), (-1, -1), 0.5, colors_palette['border']),
            ('ROWBACKGROUNDS', (0, 0), (-1, -1), [colors_palette['light'], colors_palette['light_gray']]),
        ]))
        story.append(lessons_table)

        story.append(PageBreak())

    # ===== WEEKLY PLANNING PAGE =====
    if include_weekly:
        story.append(Paragraph('📋 WEEKLY PLANNING', header_style))
        story.append(Paragraph('Week of: ________________', normal_style))
        story.append(Spacer(1, 10))

        weekly_data = [
            ['Day', 'Top 3 Priorities', 'Key Tasks', 'Notes'],
            ['Monday', '', '', ''],
            ['Tuesday', '', '', ''],
            ['Wednesday', '', '', ''],
            ['Thursday', '', '', ''],
            ['Friday', '', '', ''],
            ['Saturday', '', '', ''],
            ['Sunday', '', '', ''],
        ]

        weekly_table = Table(weekly_data, colWidths=[2.2*cm, 4.2*cm, 4.2*cm, 3.2*cm])
        weekly_table.setStyle(TableStyle([
            ('BACKGROUND', (0, 0), (-1, 0), colors_palette['accent']),
            ('TEXTCOLOR', (0, 0), (-1, 0), colors_palette['dark']),
            ('FONTNAME', (0, 0), (-1, 0), FONT_BOLD),
            ('FONTSIZE', (0, 0), (-1, 0), 12),
            ('ALIGN', (0, 0), (-1, 0), 'CENTER'),
            ('VALIGN', (0, 0), (-1, 0), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 0), (-1, 0), 10),
            ('TOPADDING', (0, 0), (-1, 0), 10),
            ('FONTNAME', (0, 1), (-1, -1), FONT_NORMAL),
            ('FONTSIZE', (0, 1), (-1, -1), 10),
            ('TEXTCOLOR', (0, 1), (-1, -1), colors_palette['dark']),
            ('ALIGN', (0, 1), (0, -1), 'CENTER'),
            ('VALIGN', (0, 1), (-1, -1), 'TOP'),
            ('BOTTOMPADDING', (0, 1), (-1, -1), 20),
            ('TOPADDING', (0, 1), (-1, -1), 8),
            ('GRID', (0, 0), (-1, -1), 0.5, colors_palette['border']),
            ('ROWBACKGROUNDS', (0, 1), (-1, -1), [colors_palette['light'], colors_palette['light_gray']]),
        ]))
        story.append(weekly_table)
        story.append(Spacer(1, 15))

        # Priority Matrix section
        story.append(Paragraph('🎯 Priority Matrix (Eisenhower Box)', section_title_style))
        story.append(Paragraph('<font size="9">Do First (Urgent+Important) | Schedule (Not Urgent+Important) | Delegate (Urgent+Not Important) | Eliminate (Not Urgent+Not Important)</font>', small_style))
        story.append(Spacer(1, 5))

        matrix_data = [
            ['Do First', 'Schedule', 'Delegate', 'Eliminate'],
            ['', '', '', ''],
            ['', '', '', ''],
            ['', '', '', ''],
            ['', '', '', ''],
        ]

        matrix_table = Table(matrix_data, colWidths=[4.5*cm, 4.5*cm, 4.5*cm, 4.5*cm])
        matrix_table.setStyle(TableStyle([
            ('BACKGROUND', (0, 0), (-1, 0), colors_palette['primary']),
            ('TEXTCOLOR', (0, 0), (-1, 0), colors_palette['light']),
            ('FONTNAME', (0, 0), (-1, 0), FONT_BOLD),
            ('FONTSIZE', (0, 0), (-1, 0), 11),
            ('ALIGN', (0, 0), (-1, 0), 'CENTER'),
            ('VALIGN', (0, 0), (-1, 0), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 0), (-1, 0), 8),
            ('TOPADDING', (0, 0), (-1, 0), 8),
            ('FONTNAME', (0, 1), (-1, -1), FONT_NORMAL),
            ('FONTSIZE', (0, 1), (-1, -1), 10),
            ('ALIGN', (0, 1), (-1, -1), 'CENTER'),
            ('VALIGN', (0, 1), (-1, -1), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 1), (-1, -1), 24),
            ('TOPADDING', (0, 1), (-1, -1), 5),
            ('GRID', (0, 0), (-1, -1), 0.5, colors_palette['border']),
            ('ROWBACKGROUNDS', (0, 1), (-1, -1), [colors_palette['light'], colors_palette['light_gray']]),
        ]))
        story.append(matrix_table)

        story.append(PageBreak())

    # ===== GENERAL TASKS PAGE =====
    if include_general:
        story.append(Paragraph('📝 GENERAL TASKS', header_style))
        story.append(Paragraph('Project: _______________________', normal_style))
        story.append(Spacer(1, 10))

        general_data = [
            ['☐', 'Pri.', 'Task Description', 'Due Date', 'Category', 'Status'],
        ] + [['', ['🅷️', '🅼️', '🅻️'][i % 3], '', '', '', 'Pending'] for i in range(12)]

        general_table = Table(general_data, colWidths=[0.8*cm, 1.0*cm, 7.2*cm, 2.0*cm, 2.0*cm, 1.8*cm])
        general_table.setStyle(TableStyle([
            ('BACKGROUND', (1, 0), (-1, 0), colors_palette['primary']),
            ('TEXTCOLOR', (1, 0), (-1, 0), colors_palette['light']),
            ('FONTNAME', (1, 0), (-1, 0), FONT_BOLD),
            ('FONTSIZE', (1, 0), (-1, 0), 11),
            ('ALIGN', (1, 0), (-1, 0), 'CENTER'),
            ('VALIGN', (0, 0), (-1, 0), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 0), (-1, 0), 8),
            ('TOPADDING', (0, 0), (-1, 0), 8),
            ('FONTNAME', (0, 1), (-1, -1), FONT_NORMAL),
            ('FONTSIZE', (0, 1), (-1, -1), 9),
            ('TEXTCOLOR', (0, 1), (-1, -1), colors_palette['dark']),
            ('ALIGN', (0, 1), (0, -1), 'CENTER'),
            ('ALIGN', (5, 1), (5, -1), 'CENTER'),
            ('VALIGN', (0, 1), (-1, -1), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 1), (-1, -1), 6),
            ('TOPADDING', (0, 1), (-1, -1), 6),
            ('GRID', (0, 0), (-1, -1), 0.5, colors_palette['border']),
            ('ROWBACKGROUNDS', (0, 1), (-1, -1), [colors_palette['light'], colors_palette['light_gray']]),
        ]))
        story.append(general_table)
        story.append(Spacer(1, 15))

        # Progress tracking section
        story.append(Paragraph('📈 Progress Tracking', section_title_style))
        story.append(Paragraph('Weekly Goal: __________________________________________________', normal_style))

        progress_data = [
            ['Week', 'Goal Set', 'Goal Achieved', 'Reflection'],
            ['Week 1', '', '', ''],
            ['Week 2', '', '', ''],
            ['Week 3', '', '', ''],
            ['Week 4', '', '', ''],
        ]

        progress_table = Table(progress_data, colWidths=[2.0*cm, 4.0*cm, 4.0*cm, 6.8*cm])
        progress_table.setStyle(TableStyle([
            ('BACKGROUND', (0, 0), (-1, 0), colors_palette['primary']),
            ('TEXTCOLOR', (0, 0), (-1, 0), colors_palette['light']),
            ('FONTNAME', (0, 0), (-1, 0), FONT_BOLD),
            ('FONTSIZE', (0, 0), (-1, 0), 11),
            ('ALIGN', (0, 0), (-1, 0), 'CENTER'),
            ('VALIGN', (0, 0), (-1, 0), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 0), (-1, 0), 8),
            ('TOPADDING', (0, 0), (-1, 0), 8),
            ('FONTNAME', (0, 1), (-1, -1), FONT_NORMAL),
            ('FONTSIZE', (0, 1), (-1, -1), 10),
            ('ALIGN', (0, 1), (-1, -1), 'CENTER'),
            ('VALIGN', (0, 1), (-1, -1), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 1), (-1, -1), 18),
            ('TOPADING', (0, 1), (-1, -1), 5),
            ('GRID', (0, 0), (-1, -1), 0.5, colors_palette['border']),
            ('ROWBACKGROUNDS', (0, 1), (-1, -1), [colors_palette['light'], colors_palette['light_gray']]),
        ]))
        story.append(progress_table)

        story.append(PageBreak())

    # ===== GOAL SETTING & REFLECTION PAGE =====
    if include_goals:
        story.append(Paragraph('🎯 GOAL SETTING & REFLECTION', header_style))
        story.append(Spacer(1, 10))

        story.append(Paragraph('📅 Monthly Goals', section_title_style))
        goal_data = [[''] for _ in range(10)]
        goal_table = Table(goal_data, colWidths=[15*cm])
        goal_table.setStyle(TableStyle([
            ('FONTNAME', (0, 0), (-1, -1), FONT_NORMAL),
            ('FONTSIZE', (0, 0), (-1, -1), 10),
            ('VALIGN', (0, 0), (-1, -1), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 0), (-1, -1), 20),
            ('TOPADGING', (0, 0), (-1, -1), 5),
            ('GRID', (0, 0), (-1, -1), 0.5, colors_palette['border']),
            ('ROWBACKGROUNDS', (0, 0), (-1, -1), [colors_palette['light'], colors_palette['light_gray']]),
        ]))
        story.append(goal_table)
        story.append(Spacer(1, 10))

        story.append(Paragraph('💭 Weekly Reflection', section_title_style))
        story.append(Paragraph('What went well this week?', normal_style))
        reflection_data = [[''] for _ in range(5)]
        reflection_table = Table(reflection_data, colWidths=[15*cm])
        reflection_table.setStyle(TableStyle([
            ('FONTNAME', (0, 0), (-1, -1), FONT_NORMAL),
            ('FONTSIZE', (0, 0), (-1, -1), 10),
            ('VALIGN', (0, 0), (-1, -1), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 0), (-1, -1), 20),
            ('TOPADGING', (0, 0), (-1, -1), 5),
            ('GRID', (0, 0), (-1, -1), 0.5, colors_palette['border']),
            ('ROWBACKGROUNDS', (0, 0), (-1, -1), [colors_palette['light'], colors_palette['light_gray']]),
        ]))
        story.append(reflection_table)
        story.append(Spacer(1, 5))

        story.append(Paragraph('What needs improvement?', normal_style))
        reflection_data2 = [[''] for _ in range(5)]
        reflection_table2 = Table(reflection_data2, colWidths=[15*cm])
        reflection_table2.setStyle(TableStyle([
            ('FONTNAME', (0, 0), (-1, -1), FONT_NORMAL),
            ('FONTSIZE', (0, 0), (-1, -1), 10),
            ('VALIGN', (0, 0), (-1, -1), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 0), (-1, -1), 20),
            ('TOPADGING', (0, 0), (-1, -1), 5),
            ('GRID', (0, 0), (-1, -1), 0.5, colors_palette['border']),
            ('ROWBACKGROUNDS', (0, 0), (-1, -1), [colors_palette['light'], colors_palette['light_gray']]),
        ]))
        story.append(reflection_table2)
        story.append(Spacer(1, 10))

        story.append(Paragraph('💡 Ideas & Inspiration', normal_style))
        ideas_data = [[''] for _ in range(3)]
        ideas_table = Table(ideas_data, colWidths=[15*cm])
        ideas_table.setStyle(TableStyle([
            ('FONTNAME', (0, 0), (-1, -1), FONT_NORMAL),
            ('FONTSIZE', (0, 0), (-1, -1), 10),
            ('VALIGN', (0, 0), (-1, -1), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 0), (-1, -1), 22),
            ('TOPADGING', (0, 0), (-1, -1), 5),
            ('GRID', (0, 0), (-1, -1), 0.5, colors_palette['border']),
            ('ROWBACKGROUNDS', (0, 0), (-1, -1), [colors_palette['light'], colors_palette['light_gray']]),
        ]))
        story.append(ideas_table)

        story.append(PageBreak())

    # ===== HABIT TRACKER PAGE =====
    if include_habits:
        story.append(Paragraph('🔄 HABIT TRACKER', header_style))
        story.append(Paragraph('Month: ________________ Year: ________________', normal_style))
        story.append(Spacer(1, 10))

        story.append(Paragraph('Track your daily habits', section_title_style))
        story.append(Spacer(1, 5))

        habit_header = ['Habit'] + [f'{i}' for i in range(1, 32)]
        habits = [
            '💧 Drink 8 glasses of water', '📖 Read for 30 min', '🏃 Exercise',
            '😴 7-8 hours sleep', '🧘 Mindfulness', '📝 Write daily tasks',
            '🌱 Take vitamins', '📵 No phone 1hr before bed', '💰 Save money',
            '📧 Clear inbox'
        ]
        habit_data = [habit_header]
        for habit in habits:
            habit_data.append([habit] + [''] * 31)

        col_widths = [5.0*cm] + [0.42*cm] * 31
        habit_table = Table(habit_data, colWidths=col_widths)
        habit_table.setStyle(TableStyle([
            ('BACKGROUND', (0, 0), (-1, 0), colors_palette['primary']),
            ('TEXTCOLOR', (0, 0), (-1, 0), colors_palette['light']),
            ('FONTNAME', (0, 0), (-1, 0), FONT_BOLD),
            ('FONTSIZE', (0, 0), (-1, 0), 8),
            ('ALIGN', (0, 0), (-1, 0), 'CENTER'),
            ('VALIGN', (0, 0), (-1, 0), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 0), (-1, 0), 5),
            ('TOPADGING', (0, 0), (-1, 0), 5),
            ('FONTNAME', (0, 1), (-1, -1), FONT_NORMAL),
            ('FONTSIZE', (0, 1), (-1, -1), 7),
            ('TEXTCOLOR', (0, 1), (-1, -1), colors_palette['dark']),
            ('ALIGN', (0, 1), (0, -1), 'LEFT'),
            ('VALIGN', (0, 1), (-1, -1), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 1), (-1, -1), 4),
            ('TOPADGING', (0, 1), (-1, -1), 3),
            ('GRID', (0, 0), (-1, -1), 0.3, colors_palette['border']),
            ('ROWBACKGROUNDS', (0, 1), (-1, -1), [colors_palette['light'], colors_palette['light_gray']]),
        ]))
        story.append(habit_table)
        story.append(Spacer(1, 15))

        # Monthly overview
        story.append(Paragraph('📊 Monthly Overview', section_title_style))
        monthly_data = [
            ['Month', 'Habits Tracked', 'Success Rate', 'Next Month Focus'],
        ] + [[m, '', '', ''] for m in ['January', 'February', 'March', 'April', 'May', 'June',
                                       'July', 'August', 'September', 'October', 'November', 'December']]

        monthly_table = Table(monthly_data, colWidths=[2.0*cm, 3.5*cm, 3.5*cm, 5.5*cm])
        monthly_table.setStyle(TableStyle([
            ('BACKGROUND', (0, 0), (-1, 0), colors_palette['primary']),
            ('TEXTCOLOR', (0, 0), (-1, 0), colors_palette['light']),
            ('FONTNAME', (0, 0), (-1, 0), FONT_BOLD),
            ('FONTSIZE', (0, 0), (-1, 0), 11),
            ('ALIGN', (0, 0), (-1, 0), 'CENTER'),
            ('VALIGN', (0, 0), (-1, 0), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 0), (-1, 0), 8),
            ('TOPADGING', (0, 0), (-1, 0), 8),
            ('FONTNAME', (0, 1), (-1, -1), FONT_NORMAL),
            ('FONTSIZE', (0, 1), (-1, -1), 9),
            ('TEXTCOLOR', (0, 1), (-1, -1), colors_palette['dark']),
            ('ALIGN', (0, 1), (-1, -1), 'CENTER'),
            ('VALIGN', (0, 1), (-1, -1), 'MIDDLE'),
            ('BOTTOMPADDING', (0, 1), (-1, -1), 6),
            ('TOPADGING', (0, 1), (-1, -1), 6),
            ('GRID', (0, 0), (-1, -1), 0.5, colors_palette['border']),
            ('ROWBACKGROUNDS', (0, 1), (-1, -1), [colors_palette['light'], colors_palette['light_gray']]),
        ]))
        story.append(monthly_table)

    # Build PDF with page decorator (footer with page number)
    def add_page_decor(canvas_obj, doc_obj):
        canvas_obj.saveState()
        canvas_obj.setFont(FONT_NORMAL, 8)
        canvas_obj.setFillColor(colors.HexColor('#AAAAAA'))
        page_num = canvas_obj.getPageNumber()
        canvas_obj.drawCentredString(105 * mm, 12 * mm, f"Page {page_num}")
        canvas_obj.setStrokeColor(colors_palette['border'])
        canvas_obj.setLineWidth(0.5)
        canvas_obj.line(25, 25, page_size[0] - 25, 25)
        canvas_obj.restoreState()

    doc.build(story, onFirstPage=add_page_decor, onLaterPages=add_page_decor)

    return output_path


# if __name__ == "__main__":
    # Create the PDF with default settings
    pdf_path = create_stylish_todo_list()
    print(f"✅ Stylish To-Do List PDF created at: {pdf_path}")
    print(f"📄 File size: {os.path.getsize(pdf_path)} bytes ({os.path.getsize(pdf_path)/1024:.1f} KB)")

# if __name__ == "__main__":
    # Create the PDF with default settings
    # pdf_path = create_stylish_todo_list()
    # print(f"✅ Stylish To-Do List PDF created at: {pdf_path}")
    # print(f"📄 File size: {os.path.getsize(pdf_path)} bytes ({os.path.getsize(pdf_path)/1024:.1f} KB)")


if __name__ == "__main__":
    # Test with default settings
    pdf_path = create_stylish_todo_list()
    print(f"✅ Stylish To-Do List PDF created at: {pdf_path}")
    print(f"📄 File size: {os.path.getsize(pdf_path)} bytes ({os.path.getsize(pdf_path)/1024:.1f} KB)")

    # Test with custom settings
    custom_path = create_stylish_todo_list(
        output_path='./outputs/todo_custom.pdf',
        primary_color='#2E86AB',
        accent_color='#A23B72'
    )
    print(f"\n✅ Custom PDF created at: {custom_path}")
    print(f"📄 File size: {os.path.getsize(custom_path)} bytes ({os.path.getsize(custom_path)/1024:.1f} KB)")
