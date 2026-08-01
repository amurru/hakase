import requests
import matplotlib.pyplot as plt
import pandas as pd
from datetime import datetime, timedelta
import os
import time

def fetch_crypto_prices(coin_id, days=7, currency='usd'):
    """
    Fetch daily cryptocurrency price data from CoinGecko API.

    Parameters:
        coin_id (str): CoinGecko coin identifier (e.g., 'bitcoin', 'ethereum').
        days (int): Number of days of historical data to fetch. Default is 7.
        currency (str): The target currency for prices. Default is 'usd'.

    Returns:
        list of tuples: Each tuple is (timestamp_ms, price) sorted by time.
                        Returns None if the API request fails.
    """
    url = f"https://api.coingecko.com/api/v3/coins/{coin_id}/market_chart"
    params = {
        'vs_currency': currency,
        'days': str(days),
        'interval': 'daily'
    }
    response = requests.get(url, params=params, timeout=30)
    if response.status_code != 200:
        print(f"Error fetching {coin_id}: {response.status_code} - {response.text}")
        return None
    data = response.json()
    return data['prices']


def fetch_multiple_crypto_prices(coin_specs, days=7, currency='usd', delay=1.0):
    """
    Fetch price data for multiple cryptocurrencies from CoinGecko API.

    Parameters:
        coin_specs (list of dict): Each dict must have keys 'id' (CoinGecko coin
            ID string) and 'symbol' (display symbol, e.g., 'BTC').
        days (int): Number of days of historical data to fetch. Default is 7.
        currency (str): The target currency for prices. Default is 'usd'.
        delay (float): Seconds to wait between API calls to respect rate limits.
            Default is 1.0.

    Returns:
        dict: Maps coin symbol -> pandas DataFrame with columns:
            ['date', 'date_str', 'price'] — one row per unique day.
    """
    results = {}
    for spec in coin_specs:
        coin_id = spec['id']
        symbol = spec['symbol']
        print(f"Fetching {symbol} ({coin_id}) data for past {days} days...")
        raw_prices = fetch_crypto_prices(coin_id, days=days, currency=currency)
        if raw_prices is None:
            print(f"  WARNING: Failed to fetch data for {coin_id}")
            continue
        df = pd.DataFrame(raw_prices, columns=['timestamp', 'price'])
        df['date'] = pd.to_datetime(df['timestamp'], unit='ms').dt.date
        # Drop duplicate dates (keep last entry for each day)
        df = df.drop_duplicates(subset='date', keep='last').reset_index(drop=True)
        df['date_str'] = df['date'].apply(
            lambda d: datetime.combine(d, datetime.min.time()).strftime('%a\n%b %d')
        )
        results[symbol] = df
        print(f"  Got {len(df)} data points for {symbol}")
        time.sleep(delay)
    return results


def plot_crypto_charts(coin_data, output_path='./outputs/crypto_price_charts.png',
                       figsize=(14, 12), dpi=150, title=None,
                       colors=None, show_summary=True):
    """
    Generate and save multi-cryptocurrency price charts as a PNG file.

    Creates a vertically-stacked subplot per coin, each showing the daily
    closing price with markers, fill area, min/max annotations, and a
    summary text footer with weekly change and price ranges.

    Parameters:
        coin_data (dict): Maps coin symbol (str) -> pandas DataFrame with
            columns 'date_str' (str) and 'price' (float).
        output_path (str): Path to save the resulting PNG. Default is
            './outputs/crypto_price_charts.png'.
        figsize (tuple): Figure size in inches. Default is (14, 12).
        dpi (int): Resolution in dots per inch for the saved image. Default 150.
        title (str or None): Overall chart title. If None, a default summary
            title is generated.
        colors (dict or None): Maps coin symbol -> hex color string for the
            price line. Default is a preset palette.
        show_summary (bool): Whether to show weekly summary statistics
            printed to stdout. Default is True.

    Returns:
        str: The path where the PNG was saved.
    """
    if not coin_data:
        raise ValueError("No cryptocurrency data provided to plot.")

    n_coins = len(coin_data)

    # Default color palette
    default_colors = {
        'BTC': '#f7931a',
        'ETH': '#627eea',
        'ADA': '#003366',
        'SOL': '#6366f1',
        'XRP': '#000000',
        'DOGE': '#c2a24d',
        'AVAX': '#e8414e',
        'DOT': '#e1004c',
    }
    if colors is None:
        colors = default_colors

    fig, axes = plt.subplots(n_coins, 1, figsize=figsize, squeeze=False)
    axes = axes.flatten()

    if title is None:
        # Build a date range for the default title
        all_dates = []
        for df in coin_data.values():
            all_dates.extend(df['date'].tolist())
        if all_dates:
            min_date = min(all_dates)
            max_date = max(all_dates)
            title = f'Cryptocurrency Price Charts - {min_date.strftime("%b %d")} to {max_date.strftime("%b %d, %Y")}'
        else:
            title = 'Cryptocurrency Price Charts'

    fig.suptitle(title, fontsize=20, fontweight='bold', y=0.98)

    summary_parts = []

    for idx, (symbol, df) in enumerate(coin_data.items()):
        ax = axes[idx]
        dates = df['date_str'].tolist()
        prices = df['price'].values
        color = colors.get(symbol, '#333333')

        ax.plot(dates, prices, marker='o', linewidth=2.5, markersize=10,
                color=color, markerfacecolor='#fff', markeredgewidth=2.5,
                label=f'{symbol} Close Price')
        ax.fill_between(range(len(dates)), prices, alpha=0.15, color=color)

        for i, price in enumerate(prices):
            label_str = rf'\${price:,.2f}'
            ax.annotate(label_str, (i, price), textcoords="offset points",
                        xytext=(0, 14), ha='center', fontsize=9,
                        fontweight='bold', color=color)

        coin_name_full = {'BTC': 'Bitcoin', 'ETH': 'Ethereum'}.get(symbol,
                                                                   symbol)
        ax.set_title(f'{coin_name_full} ({symbol}) - Daily Close Price',
                     fontsize=15, fontweight='bold', pad=15)
        ax.set_ylabel('Price (USD)', fontsize=13)
        ax.grid(True, alpha=0.3, linestyle='--')
        ax.legend(loc='upper left', fontsize=12)

        price_range = max(prices) - min(prices)
        ax.set_ylim(min(prices) - price_range * 0.08,
                    max(prices) + price_range * 0.10)

        min_idx = prices.argmin()
        max_idx = prices.argmax()
        ax.annotate(rf'Low: \${prices[min_idx]:,.2f}',
                    xy=(min_idx, prices[min_idx]),
                    xytext=(min_idx + 0.2, prices[min_idx] - price_range * 0.05),
                    fontsize=9, color='#ff6b35', fontweight='bold',
                    arrowprops=dict(arrowstyle='->', color='#ff6b35', lw=1.5))
        ax.annotate(rf'High: \${prices[max_idx]:,.2f}',
                    xy=(max_idx, prices[max_idx]),
                    xytext=(max_idx - 0.2, prices[max_idx] + price_range * 0.05),
                    fontsize=9, color='#00c853', fontweight='bold',
                    arrowprops=dict(arrowstyle='->', color='#00c853', lw=1.5))

        # Last price and change for summary
        last_price = prices[-1]
        first_price = prices[0]
        pct_change = ((last_price - first_price) / first_price) * 100
        summary_parts.append(
            rf'{symbol}: \${last_price:,.2f} ({pct_change:+.2f}%)'
        )

    # Set xlabel only on the bottom subplot
    axes[-1].set_xlabel('Date', fontsize=13)

    # Hide any unused subplots
    for idx in range(n_coins, len(axes)):
        axes[idx].set_visible(False)

    # Summary text at the bottom
    if show_summary:
        summary_str = ' | '.join(summary_parts)
        range_parts = []
        for symbol, df in coin_data.items():
            prices = df['price'].values
            range_parts.append(
                rf'{symbol} Range: \${min(prices):,.2f} - \${max(prices):,.2f}'
            )
        full_summary = rf'Weekly Summary | {summary_str} | {" | ".join(range_parts)}'
        fig.text(0.5, 0.02, full_summary, ha='center', fontsize=11,
                 style='italic', color='#555555')

    # Adjust layout to make room for summary text
    bottom_margin = 0.05 if show_summary else 0.03
    plt.tight_layout(rect=[0, bottom_margin + 0.01, 1, 0.95])

    # Ensure output directory exists
    out_dir = os.path.dirname(output_path)
    if out_dir:
        os.makedirs(out_dir, exist_ok=True)

    plt.savefig(output_path, dpi=dpi, bbox_inches='tight', facecolor='white')
    plt.close()

    return output_path


def plot_crypto_price_charts(coin_symbols=('BTC', 'ETH'), days=7,
                             output_path='./outputs/crypto_price_charts.png',
                             figsize=(14, 12), dpi=150, currency='usd',
                             delay=1.0, show_summary=True):
    """
    Fetch cryptocurrency prices and generate/save a combined price chart PNG.

    This is the main convenience function that ties together data fetching
    from the CoinGecko API and chart rendering with matplotlib.

    Parameters:
        coin_symbols (tuple): Coin symbols to plot. Each must be one of the
            supported CoinGecko symbols: 'BTC', 'ETH', 'ADA', 'SOL', 'XRP',
            'DOGE', 'AVAX', 'DOT'. Default is ('BTC', 'ETH').
        days (int): Number of days of historical price data to fetch.
            Default is 7.
        output_path (str): Path to save the PNG. Default is
            './outputs/crypto_price_charts.png'.
        figsize (tuple): Figure size in inches. Default is (14, 12).
        dpi (int): Resolution for the saved PNG. Default is 150.
        currency (str): Quote currency for prices. Default is 'usd'.
        delay (float): Seconds between API calls. Default is 1.0.
        show_summary (bool): Whether to print weekly summary to stdout.
            Default is True.

    Returns:
        dict with keys:
            'output_path' (str): Path to saved PNG.
            'coin_data' (dict): Fetched data per coin symbol.
            'summary' (dict): Per-coin last price and % change.
    """
    # Map symbols to CoinGecko coin IDs
    symbol_to_id = {
        'BTC': 'bitcoin',
        'ETH': 'ethereum',
        'ADA': 'cardano',
        'SOL': 'solana',
        'XRP': 'ripple',
        'DOGE': 'dogecoin',
        'AVAX': 'avalanche-2',
        'DOT': 'polkadot',
    }

    coin_specs = []
    for symbol in coin_symbols:
        symbol_upper = symbol.upper()
        coin_id = symbol_to_id.get(symbol_upper)
        if coin_id is None:
            print(f"ERROR: Unsupported coin symbol '{symbol}'. "
                  f"Supported: {list(symbol_to_id.keys())}")
            continue
        coin_specs.append({'id': coin_id, 'symbol': symbol_upper})

    if not coin_specs:
        raise ValueError("No valid coin symbols provided.")

    # Fetch all price data
    coin_data = fetch_multiple_crypto_prices(
        coin_specs, days=days, currency=currency, delay=delay
    )

    if not coin_data:
        raise RuntimeError("Failed to fetch data for any cryptocurrency.")

    # Build per-coin summary
    summary = {}
    for symbol, df in coin_data.items():
        prices = df['price'].values
        pct = ((prices[-1] - prices[0]) / prices[0]) * 100
        summary[symbol] = {
            'start_price': prices[0],
            'end_price': prices[-1],
            'pct_change': pct,
            'min_price': min(prices),
            'max_price': max(prices),
        }

    # Plot and save
    saved_path = plot_crypto_charts(
        coin_data, output_path=output_path, figsize=figsize,
        dpi=dpi, colors=None, show_summary=show_summary
    )

    if show_summary:
        print(f"\n{'=' * 60}")
        print(f"Chart saved successfully to: {saved_path}")
        print(f"{'=' * 60}")
        print(f"\n--- Weekly Summary ---")
        for symbol, info in summary.items():
            print(f"{symbol}: ${info['start_price']:,.2f} -> "
                  f"${info['end_price']:,.2f} ({info['pct_change']:+.2f}%)")
            print(f"  Range: ${info['min_price']:,.2f} - "
                  f"${info['max_price']:,.2f}")

        if os.path.exists(saved_path):
            file_size = os.path.getsize(saved_path)
            print(f"\nFile size: {file_size / 1024:.1f} KB")

    return {
        'output_path': saved_path,
        'coin_data': coin_data,
        'summary': summary,
    }


# Example usage / demo
if __name__ == "__main__":
    # This week: 7 days of BTC and ETH data from CoinGecko
    result = plot_crypto_price_charts(
        coin_symbols=('BTC', 'ETH'),
        days=7,
        output_path='./outputs/btc_eth_price_charts.png',
        show_summary=True
    )