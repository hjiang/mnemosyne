{
  description = "Mnemosyne — self-hosted IMAP backup and search";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };

        mnemosyne = pkgs.buildGoModule {
          pname = "mnemosyne";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-6Fej/NQjpBnG6dMZmd00eQno/7nUJY0u+/Qh725lEqY="; # will need update after adding oauth deps
          subPackages = [ "cmd/mnemosyne" ];
          env.CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" ];
        };
      in
      {
        packages.default = mnemosyne;

        packages.docker = pkgs.dockerTools.buildLayeredImage {
          name = "mnemosyne";
          tag = "latest";
          contents = [
            mnemosyne
            pkgs.poppler-utils
            pkgs.cacert
            pkgs.tzdata
          ];
          config = {
            Entrypoint = [ "mnemosyne" ];
            Cmd = [ "serve" ];
            ExposedPorts."8080/tcp" = {};
            Env = [
              "MNEMOSYNE_DATA_DIR=/var/lib/mnemosyne"
              "MNEMOSYNE_LISTEN=:8080"
              "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
            ];
            Volumes."/var/lib/mnemosyne" = {};
          };
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            # Go toolchain
            go
            gopls
            delve

            # Linting
            golangci-lint

            # Runtime dependencies
            sqlite
            poppler-utils  # pdftotext for PDF text extraction
          ];

          shellHook = ''
            export GOPATH="$PWD/.go"
            export PATH="$GOPATH/bin:$PATH"
          '';
        };
      });
}
