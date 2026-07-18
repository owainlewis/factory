use std::path::PathBuf;

use anyhow::Result;
use clap::{Parser, Subcommand};

use factory::config::{Config, default_config_path};

#[derive(Debug, Parser)]
#[command(name = "factory", version, about)]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Debug, Subcommand)]
enum Command {
    /// Validate configuration without starting workers or network activity.
    Validate {
        /// Path to the Factory configuration file.
        #[arg(long)]
        config: Option<PathBuf>,
    },
}

fn main() -> Result<()> {
    let cli = Cli::parse();

    match cli.command {
        Command::Validate { config } => {
            let path = config.unwrap_or_else(default_config_path);
            let config = Config::load(&path)?;
            print!("{config}");
        }
    }

    Ok(())
}
