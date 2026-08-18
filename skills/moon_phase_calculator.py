"""Moon phase calculator.

Computes the lunar phase for any given date/time using simplified
astronomical formulas: the mean Sun-Earth-Moon elongation, the
illuminated fraction of the Moon as seen from Earth, and the
traditional eight-phase name (new / waxing crescent / first quarter /
waxing gibbous / full / waning gibbous / last quarter / waning
crescent). Pure Python standard library - no third-party dependencies.
"""

import math
from datetime import datetime, timezone


def julian_date(dt):
    """Convert a datetime to a Julian date (including the fractional day).

    Parameters:
        dt (datetime): The moment to convert (timezone-aware preferred).

    Returns:
        float: The Julian date, e.g. 2461200.5 for 2026-08-17 00:00 UTC.

    Usage:
        from skills.moon_phase_calculator import julian_date
        jd = julian_date(datetime(2026, 8, 17, 12, 0))
    """
    a = (14 - dt.month) // 12
    y = dt.year + 4800 - a
    m = dt.month + 12 * a - 3
    jdn = dt.day + (153 * m + 2) // 5 + 365 * y + y // 4 - y // 100 + y // 400 - 32045
    return jdn + (dt.hour - 12) / 24 + dt.minute / 1440 + dt.second / 86400


def phase_name_for_elongation(elongation_deg):
    """Map a Sun-Earth-Moon elongation angle (degrees) to the traditional
    eight-phase lunar name.

    Elongation is 0 deg at new moon and 180 deg at full moon. Each phase
    spans 45 degrees, centered on 0/45/90/135/180/225/270/315 degrees.

    Parameters:
        elongation_deg (float): Mean elongation D in degrees, in [0, 360).

    Returns:
        str: One of the eight traditional phase names.

    Usage:
        from skills.moon_phase_calculator import phase_name_for_elongation
        name = phase_name_for_elongation(56.1)   # -> 'Waxing Crescent'
    """
    d = elongation_deg % 360
    if d < 22.5:
        return "New Moon"
    if d < 67.5:
        return "Waxing Crescent"
    if d < 112.5:
        return "First Quarter"
    if d < 157.5:
        return "Waxing Gibbous"
    if d < 202.5:
        return "Full Moon"
    if d < 247.5:
        return "Waning Gibbous"
    if d < 292.5:
        return "Last Quarter"
    return "Waning Crescent"


def lunar_phase_details(dt=None):
    """Compute the lunar phase for a given moment.

    Uses Meeus-style truncated series for the Moon's mean elongation
    from the Sun, which determines the illuminated fraction and the
    traditional phase name.

    Parameters:
        dt (datetime or None): The moment to compute the phase for.
            Naive datetimes are treated as UTC. Defaults to the current
            time in UTC.

    Returns:
        dict: {
            'datetime': datetime,                # UTC moment used
            'elongation_deg': float,             # mean Sun-Earth-Moon elongation D in [0, 360)
            'sun_selenographic_longitude_deg': float,  # sun's longitude at the moon, measured from the Earth direction (= D)
            'illuminated_fraction': float,       # 0.0 (new moon) .. 1.0 (full moon)
            'phase_name': str,                   # one of the eight traditional phase names
        }

    Usage:
        from skills.moon_phase_calculator import lunar_phase_details
        details = lunar_phase_details()                              # now (UTC)
        details = lunar_phase_details(datetime(2026, 8, 17, 12, 0))  # a specific moment
        print(details['phase_name'], details['illuminated_fraction'])
    """
    if dt is None:
        dt = datetime.now(timezone.utc)
    elif dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)

    jd = julian_date(dt)
    T = (jd - 2451545.0) / 36525.0

    # Mean elongation of the Moon from the Sun (Sun-Earth-Moon angle),
    # in degrees: 0 at new moon, 180 at full moon.
    D = (297.85036 + 445267.1115 * T - 0.001914 * T**2 + 0.000018 * T**3) % 360

    # Illuminated fraction as seen from Earth: k = (1 - cos D) / 2.
    # D = 0 deg -> new moon (0% illuminated); D = 180 deg -> full moon (100%).
    illuminated = (1 - math.cos(math.radians(D))) / 2

    return {
        "datetime": dt,
        "elongation_deg": round(D, 2),
        "sun_selenographic_longitude_deg": round(D, 2),
        "illuminated_fraction": round(illuminated, 4),
        "phase_name": phase_name_for_elongation(D),
    }


def format_phase_report(details):
    """Render lunar phase details as a human-readable multi-line string.

    Parameters:
        details (dict): Output of lunar_phase_details().

    Returns:
        str: A formatted report line-by-line, e.g.
            'Current date: 2026-08-17 18:22 UTC'
            'Phase: Waxing Crescent (22.1% illuminated)'.

    Usage:
        from skills.moon_phase_calculator import format_phase_report, lunar_phase_details
        print(format_phase_report(lunar_phase_details()))
    """
    dt = details["datetime"]
    return (
        f"Current date: {dt.strftime('%Y-%m-%d %H:%M')} UTC\n"
        f"Mean elongation (D): {details['elongation_deg']:.1f} deg\n"
        f"Sun's selenographic longitude: {details['sun_selenographic_longitude_deg']:.1f} deg\n"
        f"Illuminated fraction: {details['illuminated_fraction']:.3f}\n"
        f"Phase: {details['phase_name']}\n"
        f"Phase percentage: {details['illuminated_fraction'] * 100:.1f}%"
    )


if __name__ == "__main__":
    # Demo: report the phase for right now (UTC). Parameterize by passing
    # a datetime to lunar_phase_details() for any other moment.
    print(format_phase_report(lunar_phase_details()))