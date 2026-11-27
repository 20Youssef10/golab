# Fake upstream CLI source for compat refresh tests ONLY.
# Not captured from the real repository. It mimics the Typer shape of
# src/colab_cli/cli.py so the conservative literal-token parser can be tested
# without a live network call. A real refresh fetches the official raw file.
import typer
from typing_extensions import Annotated

app = typer.Typer(help="Colab CLI")


@app.callback()
def callback(
    ctx: typer.Context,
    client_oauth_config: Annotated[
        str, typer.Option("-c", "--client-oauth-config", help="OAuth config")
    ],
    config: Annotated[str, typer.Option("--config")] = None,
    public_api: Annotated[
        bool,
        typer.Option("--public-api/--no-public-api", envvar="COLAB_CLI_PUBLIC_API"),
    ] = False,
):
    """Global options."""
    pass


@app.command(name="help")
def help_command(ctx: typer.Context):
    """Show help."""
    pass


@app.command()
def fresh(
    only: Annotated[bool, typer.Option("--only")] = False,
):
    """A command whose name is derived from the function name."""
    pass


app.command()(registered)


def registered(
    session: Annotated[str, typer.Option("-s", "--session")],
):
    """A command registered via the app.command()(fn) form."""
    pass


# Decoys: these are not typer.Option declarations and must not be captured.
DECOY = "--decoy-flag"
# --comment-flag
