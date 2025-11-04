# m_create_audio_buffer.py
from typing import List, Optional
import numpy as np

from stream_pipeline.data_package import DataPackage, DataPackageController, DataPackagePhase, DataPackageModule, Status
from stream_pipeline.module_classes import ExecutionModule, ModuleOptions

from ogg import Ogg_OPUS_Audio, OggS_Page, calculate_page_duration
import data
import logger

log = logger.get_logger()

class Create_Audio_Buffer(ExecutionModule):
    def __init__(self,
                    last_n_seconds: int = 10,
                    min_n_seconds: int = 1,
                    sample_rate: int = 48000  # Add sample_rate parameter for PCM
                ) -> None:
        super().__init__(ModuleOptions(
                                use_mutex=False,
                                timeout=5,
                            ),
                            name="Audio-Buffer-Module"
                        )

        self.last_n_seconds: int = last_n_seconds
        self.min_n_seconds: int = min_n_seconds
        self.sample_rate: int = sample_rate

        self._audio_data_buffer: List[bytes] = []  # Change to store raw audio chunks
        self._current_audio_buffer_seconds: float = 0
        self._is_pcm: bool = False  # Track if we're dealing with PCM
        self._bytes_per_second: int = sample_rate * 2  # 16-bit PCM = 2 bytes per sample

    def execute(self, dp: DataPackage[data.AudioData], dpc: DataPackageController, dpp: DataPackagePhase, dpm: DataPackageModule) -> None:
        if not dp.data:
            raise Exception("No data found")
        if not dp.data.raw_audio_data:
            raise Exception("No audio data found")

        audio_data = dp.data.raw_audio_data

        # Detect if this is PCM data (not OGG)
        if not self._is_pcm:
            # Check if it's OGG format
            if len(audio_data) >= 4 and audio_data[:4] == b'OggS':
                self._is_pcm = False
                # Handle OGG format (existing code)
                self._handle_ogg_format(dp, audio_data, dpm)
                return
            else:
                self._is_pcm = True
                log.info("Detected PCM audio format")

        # Handle PCM format
        if self._is_pcm:
            self._handle_pcm_format(dp, audio_data, dpm)

    def _handle_ogg_format(self, dp: DataPackage[data.AudioData], audio_data: bytes, dpm: DataPackageModule) -> None:
        """Handle OGG format (existing functionality)"""
        try:
            page = OggS_Page(audio_data)
            # ... rest of your existing OGG processing code
        except Exception as e:
            dpm.message = f"OGG processing error: {e}"
            dpm.status = Status.EXIT

    def _handle_pcm_format(self, dp: DataPackage[data.AudioData], audio_data: bytes, dpm: DataPackageModule) -> None:
        """Handle raw PCM audio format"""
        # Add PCM data to buffer
        self._audio_data_buffer.append(audio_data)
        
        # Calculate duration of this chunk (4096 bytes = 2048 samples for 16-bit mono)
        chunk_duration = len(audio_data) / self._bytes_per_second
        self._current_audio_buffer_seconds += chunk_duration

        # Check if we have enough audio to process
        if self._current_audio_buffer_seconds >= self.min_n_seconds:
            # If buffer is too long, remove oldest chunks
            while self._current_audio_buffer_seconds >= self.last_n_seconds and len(self._audio_data_buffer) > 1:
                removed_chunk = self._audio_data_buffer.pop(0)
                removed_duration = len(removed_chunk) / self._bytes_per_second
                self._current_audio_buffer_seconds -= removed_duration

            # Combine audio chunks for processing
            combined_audio = b''.join(self._audio_data_buffer)
            
            # Update the data package with PCM audio
            dp.data.raw_audio_data = combined_audio
            dp.data.audio_buffer_time = self._current_audio_buffer_seconds
            dp.data.audio_buffer_start_after = 0.0  # For PCM, we don't have granule positions
            dp.data.audio_data_sample_rate = self.sample_rate
            
            # Convert PCM to numpy array for downstream processing
            audio_array = np.frombuffer(combined_audio, dtype=np.int16)
            dp.data.audio_data = audio_array.astype(np.float32) / 32768.0
            
        else:
            dpm.status = Status.EXIT
            dpm.message = "Not enough PCM audio data to create a package"
