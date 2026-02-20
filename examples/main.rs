use libfetch::Api;

#[tokio::main]
async fn main() {
    // Download the latest llama.cpp release
    let result = Api::new()
        .set_install_dir("./llamalib")
        .repo("ggml-org/llama.cpp")
        .latest()
        .install(|version| format!("llama-{version}-bin-win-cpu-x64.zip"))
        .await;

    match result {
        Ok(_) => println!("llama.cpp installed successfully"),
        Err(e) => eprintln!("error installing llama.cpp: {e}"),
    }

    // Download a specific version
    let result = Api::new()
        .set_install_dir("./libffi")
        .repo("libffi/libffi")
        .version("v3.5.1")
        .install(|version| {
            let clean = version.trim_start_matches('v');
            format!("libffi-{clean}-x86-32bit-msvc-binaries.zip")
        })
        .await;

    match result {
        Ok(_) => println!("libffi installed successfully"),
        Err(e) => eprintln!("error installing libffi: {e}"),
    }

    // Download with retry and proxy settings
    let result = Api::new()
        .set_install_dir("./sd")
        .set_retry_count(3)
        .set_retry_delay_secs(3)
        .repo("leejet/stable-diffusion.cpp")
        .latest()
        .install(|version| format!("sd-master-{version}-bin-win-avx2-x64.zip"))
        .await;

    match result {
        Ok(_) => println!("stable-diffusion.cpp installed successfully"),
        Err(e) => eprintln!("error installing stable-diffusion.cpp: {e}"),
    }
}
